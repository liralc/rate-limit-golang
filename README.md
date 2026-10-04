# Rate Limiter em Go

Middleware HTTP que limita requisições por segundo **por IP** ou **por token de acesso** (header `API_KEY`), com persistência em Redis e bloqueio temporário de quem excede o limite.

## Como executar

Pré-requisito: Docker com Docker Compose.

```bash
docker compose up -d --build      # sobe Redis + aplicação em http://localhost:8080
docker compose logs -f app        # acompanha os logs
docker compose down               # encerra
```

Testando manualmente:

```bash
# Limite por IP (padrão: 10 req/s) -> 10x 200 seguidos de 429
for i in $(seq 15); do curl -s -o /dev/null -w '%{http_code} ' localhost:8080/; done; echo

# Limite por token (abc123: 100 req/s) -> sobrepõe o limite do IP
for i in $(seq 105); do curl -s -o /dev/null -w '%{http_code}\n' -H 'API_KEY: abc123' localhost:8080/; done | sort | uniq -c
```

Depois de bloqueado, o IP ou token recebe `429` com o corpo abaixo até o fim do `BLOCK_DURATION`:

```
you have reached the maximum number of requests or actions allowed within a certain time frame
```

Para limpar os bloqueios durante os testes manuais: `docker compose exec redis redis-cli FLUSHALL`.

## Como executar os testes

```bash
docker compose --profile test run --rm --build tests
```

O serviço `tests` roda `go test -race ./...` dentro do container, com um Redis real disponível (`REDIS_TEST_ADDR`). Localmente, com Go instalado, basta `go test -race ./...`: os testes usam [miniredis](https://github.com/alicebob/miniredis) e não exigem Redis rodando (o teste contra Redis real é ignorado se `REDIS_TEST_ADDR` não estiver definido).

## Configuração

Tudo é configurado por variáveis de ambiente ou pelo arquivo `.env` na raiz (variáveis já definidas no ambiente têm precedência sobre o `.env`). Use [.env.example](.env.example) como referência.

| Variável              | Padrão           | Descrição |
|-----------------------|------------------|-----------|
| `SERVER_PORT`         | `8080`           | Porta HTTP. |
| `RATE_LIMIT_IP`       | `10`             | Máximo de requisições por segundo por IP. |
| `RATE_LIMIT_TOKENS`   | *(vazio)*        | Limites por token, no formato `token:limite` separados por vírgula. Ex.: `abc123:100,def456:50`. |
| `BLOCK_DURATION`      | `5m`             | Tempo de bloqueio após exceder o limite ([duração Go](https://pkg.go.dev/time#ParseDuration): `30s`, `5m`, `1h`). |
| `TRUST_PROXY_HEADERS` | `false`          | Usa `X-Forwarded-For` / `X-Real-IP` para obter o IP do cliente. Habilite somente atrás de um proxy confiável. |
| `STORAGE_STRATEGY`    | `redis`          | Estratégia de persistência: `redis` ou `memory`. |
| `REDIS_ADDR`          | `localhost:6379` | Endereço do Redis (no compose é sobrescrito para `redis:6379`). |
| `REDIS_PASSWORD`      | *(vazio)*        | Senha do Redis. |
| `REDIS_DB`            | `0`              | Banco do Redis. |

Configurações inválidas impedem a aplicação de iniciar, com mensagem indicando a variável incorreta.

### Regras de limitação

- Sem header `API_KEY` (ou com um token não configurado): a requisição conta para o limite do **IP**.
- Com um token configurado em `RATE_LIMIT_TOKENS`: a requisição conta para o limite do **token**, que **sobrepõe** o do IP — mesmo que o IP esteja bloqueado ou tenha limite menor.
- A contagem é feita em janelas de 1 segundo. Ao ultrapassar o limite, a chave (IP ou token) é bloqueada por `BLOCK_DURATION`; durante o bloqueio, todas as requisições recebem `429`, inclusive em janelas seguintes.

## Arquitetura

```
cmd/server/                 composição: config -> storage -> limiter -> middleware -> http.Server
internal/config/            leitura e validação de env/.env
internal/limiter/           regra de negócio + interface Storage (Strategy)
internal/middleware/        adaptação HTTP: extrai IP/token, responde 429
internal/storage/redis/     estratégia Redis
internal/storage/memory/    estratégia em memória
```

- **Desacoplamento:** `limiter` não conhece HTTP nem Redis. O `middleware` depende apenas de uma interface `Allow(ctx, ip, token)`.
- **Strategy:** o `limiter.Limiter` recebe uma implementação de `limiter.Storage`:

  ```go
  type Storage interface {
      Increment(ctx context.Context, key string, window time.Duration) (int64, error)
      Block(ctx context.Context, key string, duration time.Duration) error
      IsBlocked(ctx context.Context, key string) (bool, error)
  }
  ```

- **Redis:** o contador usa um script Lua (`INCR` + `PEXPIRE` na primeira ocorrência), atômico mesmo com várias instâncias da aplicação. O bloqueio é uma chave com TTL igual a `BLOCK_DURATION`. Chaves: `ratelimit:count:<ip|token>:<valor>` e `ratelimit:block:<ip|token>:<valor>`.

### Como trocar a estratégia de persistência

1. Para usar a estratégia em memória já existente, defina `STORAGE_STRATEGY=memory` (útil em desenvolvimento; o estado não é compartilhado entre instâncias).
2. Para adicionar outra (ex.: Memcached, PostgreSQL):
   - crie um pacote em `internal/storage/<nome>` com um tipo que implemente `limiter.Storage` (`Increment` precisa ser atômico);
   - adicione a opção em `internal/config` (`STORAGE_STRATEGY`) e um `case` em `newStorage` no [cmd/server/main.go](cmd/server/main.go).

   Nenhuma alteração é necessária no `limiter` ou no `middleware`.

## Decisões

- **Tokens desconhecidos** caem no limite do IP. Assim, enviar tokens aleatórios não permite contornar o limite por IP.
- **Falha do Redis** durante uma requisição resulta em `500` (fail-closed) e o erro é registrado em log; a aplicação não deixa passar tráfego sem controle.
- **IP do cliente** vem de `RemoteAddr` por padrão. Headers de proxy só são considerados com `TRUST_PROXY_HEADERS=true`, para evitar falsificação de IP.
- **Algoritmo:** janela fixa de 1 segundo, simples e suficiente para "requisições por segundo".
