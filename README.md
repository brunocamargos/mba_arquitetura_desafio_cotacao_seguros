# Prumo Cota: proteção da fronteira com as seguradoras

Entrega do desafio de Fundamentos de Arquitetura de Solução. O enunciado está em [docs/enunciado.md](docs/enunciado.md).

- **SAD:** [docs/sad.md](docs/sad.md)
- **Evidências:** [docs/evidencias/](docs/evidencias/), com a tabela abaixo

## O coração da solução

Protegi a fronteira entre a plataforma e as três seguradoras, porque a instabilidade delas derrubava a cotação inteira e cada consulta repetida custava R$ 0,04. A proteção combina timeout de 2 s, um circuit breaker por parceira que, aberto, não faz a chamada, um cache isolado por corretora e uma resposta parcial que diz qual seguradora faltou e por quê. Na mesma carga, o sucesso foi de 60% para 100% e o p95 de 8,01 s para 198 ms, com a ajuda de uma carga que repete 5 cotações. O custo é uma lista às vezes incompleta, cotações com até 300 s de idade e cerca de R$ 9.261/mês de operação, contra R$ 79.200/mês de economia estimada com 25% de recotação, um número que ainda precisa ser validado em produção.

## Como subir e reproduzir

Requer Docker com Compose v2 e Go 1.25.

```bash
make down && make reproduce
make test
make smoke
```

## O que foi implementado e o que ficou como proposta

| Implementado | Onde |
|---|---|
| Timeout de 2000 ms; o estouro conta como falha | `internal/partner/client.go` |
| Circuit breaker por parceira (5 falhas consecutivas, 10 s aberto, 2 sucessos para fechar) | `internal/partner/breaker.go` |
| Cache no Redis por parceira, TTL = `min(valid_for_seconds, 300 s)` | `internal/cache/cache.go` e `internal/quotation/service.go` |
| Resposta parcial e 503 quando nenhuma parceira entrega | `internal/quotation/service.go`, `internal/quotation/request.go` e `internal/quotation/handler.go` |
| Parâmetros por variável de ambiente | `internal/platform/config.go` |

**Ficou como proposta:** trilha de auditoria (SUSEP), regras de alerta carregadas no Prometheus, hospedagem em nuvem e recuperação de região. O bônus de paralelismo não foi feito.

### Chave do cache

```
quote:v1:{tenant_id}:{partner}:{risk_hash}

tenant_id = X-Tenant-Id já validado contra TENANTS
partner   = nome da parceira em PARTNER_ENDPOINTS
risk_hash = hex(SHA-256(document, birth_year, plate, model, year, value_cents, coverage))
            campos já normalizados por Request.Normalize() e separados por 0x1F
```

**Invalidação:** só por TTL. Falha não é cacheada, e entrada vencida nunca é servida, nem com o breaker aberto.

### Contrato de resposta degradada

A mudança é aditiva em `Response` (`internal/quotation/request.go`), e a justificativa está na seção 4.3 do SAD:

- `quotes[].source`: `partner` ou `cache`
- `quotes[].age_seconds`: idade da proposta
- `quotes[].valid_for_seconds`: validade restante
- `degraded`: `true` quando falta alguma parceira
- `missing[]`: `partner` e `reason` (`circuit_open`, `timeout` ou `unavailable`)

Sem nenhuma proposta, a resposta é 503 `{"error":"no partner insurer available","missing":[...]}`.

## Métricas e atributos

| Métrica no Prometheus | Instrumento OTel | Atributos |
|---|---|---|
| `quotation_breaker_state` (0 fechado, 1 meio aberto, 2 aberto) | `quotation.breaker.state` | `partner` |
| `quotation_cache_lookups_total` | `quotation.cache.lookups` | `partner`, `result` |
| `http_client_request_duration_seconds`, reaproveitada para a latência por parceira: `histogram_quantile(0.95, sum by (le, server_address) (rate(http_client_request_duration_seconds_bucket[5m])))` | SDK genérico | `server_address` |

**Atributos do span `partner.quote`:** `quotation.partner` e `quotation.outcome` (`partner`, `cache`, `circuit_open`, `timeout` ou `unavailable`). Nenhum carrega CPF, placa ou `quote_id`.

## Evidências

| Evidência | Comando ou consulta | Janela | Artefato | Legenda |
|---|---|---|---|---|
| Relatório antes/depois | `make down && make reproduce` | Toda a execução | [antes](docs/evidencias/antes-make-reproduce.txt), [depois](docs/evidencias/depois-make-reproduce.txt) | Na mesma carga, sucesso de 60% para 100%, p95 de 8,01 s para 198 ms, vazão de 8,1 para 820,1 req/s |
| Saída do `make test` | `GOFLAGS='-count=1 -v' make test` | n/a | [saída](docs/evidencias/depois-make-test.txt) | Testes originais e novos: 144 PASS, 0 FAIL, 0 SKIP |
| Trace com breaker aberto | `curl 'localhost:16686/api/traces?service=quotation-api&operation=POST%20/quotes&lookback=1h&limit=6000'` | Last Hour | [trace](docs/evidencias/depois-trace-breaker-aberto.json) | `partner-flaky` com `quotation.outcome=circuit_open`, sem span de saída, em resposta de 90 µs |
| Trace servido de cache | `curl 'localhost:16686/api/traces?service=quotation-api&operation=POST%20/quotes&lookback=1h&limit=300'` | Last Hour | [trace](docs/evidencias/depois-trace-cache.json) | Três `quotation.outcome=cache` e nenhum span de saída |
| Estado do breaker | `max by (partner) (quotation_breaker_state)` | 5m | [gráfico](docs/evidencias/depois-breaker-estado.png) | A `partner-degrading` faz fechado → aberto → meio aberto → fechado |
| Hit rate | `sum(rate(quotation_cache_lookups_total{result="hit"}[1m])) / sum(rate(quotation_cache_lookups_total[1m]))` | 5m, `[1m]` | [gráfico](docs/evidencias/depois-hit-rate.png) | Sobe de 0% para 74% conforme o cache aquece. É propriedade da carga e não entra na seção 8 do SAD |
| p95 do `POST /quotes` | Antes: `histogram_quantile(0.95, sum by (le) (rate(http_server_request_duration_seconds_bucket{http_route="/quotes"}[5m])))`. Depois: `histogram_quantile(0.95, sum by (le) (http_server_request_duration_seconds_bucket{http_route="/quotes"})) or label_replace(vector(10), "escala", "teto", "", "") or label_replace(vector(0), "escala", "piso", "", "")` | 15m | [antes](docs/evidencias/antes-p95-quotes.png), [depois](docs/evidencias/depois-p95-quotes.png) | De cerca de 9,7 s para 0,23 s, com teto de 10 s nos dois gráficos. O PromQL interpola por bucket; a comparação numérica é o relatório |

**Cargas dos gráficos, com os perfis default das parceiras:**

```bash
make down && docker compose up -d --build --wait
for i in 1 2 3 4; do make load ARGS="-baseline 0 -distinct 150 -requests 900 -concurrency 5"; done
make load ARGS="-baseline 0 -distinct 1000 -requests 400 -concurrency 50"
sleep 25
make load ARGS="-tenant corretora-b -baseline 0 -distinct 3 -requests 6 -concurrency 1"
```

O laço aquece o cache: o gráfico de hit rate foi capturado ao fim dele. Os comandos seguintes abrem o breaker e, depois de 25 s sem tráfego, fazem as sondas que o fecham.

## Limitações documentadas

1. **Testes que exigem os defeitos.** Três testes originais exigem agregação em série e o 502. Para mantê-los verdes, `NewService` conserva o tudo ou nada e a API usa `NewResilientService` (seção 5.2 do SAD).
2. **Consulta de p95 diferente no "depois".** A carga termina junto da exportação de métricas (10 s), então `rate()` não vê aumento e o "depois" usa a consulta acumulada. As séries `escala` só fixam o eixo em 0 a 10 s.
3. **Cargas separadas para breaker e hit rate.** Na carga padrão o cache cobre quase tudo, então os dois gráficos vêm das cargas declaradas acima.
4. **Escala do p95.** Os dois gráficos têm teto de 10 s, mas o eixo do "antes" começa perto de 2 s, porque foi capturado sem as séries de escala.

## O que faria diferente com mais tempo

- Validar P1 com dados reais antes de fechar a conta da seção 8.
- Limitar chamadas simultâneas por parceira, o que resolveria o limite L1 do SAD.
- Colocar as regras de alerta no Prometheus e testar cada uma disparando.
