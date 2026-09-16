# Server 설치 및 활성화

이 문서는 Docker Log Viewer의 중앙 Server를 처음 설치하는 과정을 설명합니다. 명령을 위에서부터 순서대로 실행하면 다음 구성이 완성됩니다.

- 브라우저 UI: `https://<SERVER_HOST>:18080`
- Agent 연결: `<SERVER_HOST>:19080`의 TLS 1.3
- 브라우저 인증: 사용자 이름 `admin`과 별도 Web token
- Agent 인증: Web token과 분리된 공유 Agent token
- Docker 재시작 후 자동 복구

Server는 Docker socket에 접근하지 않습니다. 각 Docker 호스트에서 실행되는 Agent가 Server로 outbound 연결을 만들고, Server는 브라우저 요청을 적절한 Agent에 전달합니다.

## 1. 준비 사항

Server 호스트에 다음 항목이 필요합니다.

- Linux
- Docker Engine
- Docker Compose v2 (`docker compose`)
- Agent 호스트에서 접근 가능한 고정 IP 또는 DNS 이름
- `openssl`과 `curl`

버전을 확인합니다.

```bash
docker version
docker compose version
openssl version
```

이 문서에서는 다음 예시 값을 사용합니다.

| 항목          | 예시                         | 설명                                            |
| ------------- | ---------------------------- | ----------------------------------------------- |
| Server 주소   | `192.168.10.10`              | Agent와 브라우저가 접근할 내부 IP 또는 DNS 이름 |
| UI 포트       | `18080`                      | 브라우저 HTTPS 포트                             |
| Agent 포트    | `19080`                      | Agent 전용 TLS 포트                             |
| 설치 디렉터리 | `~/docker-log-viewer-server` | Compose와 secret을 보관할 위치                  |

예시 IP는 실제 Server 주소로 바꾸세요. Server 주소가 바뀌면 TLS 인증서도 다시 발급해야 합니다.

## 2. 설치 디렉터리 생성

```bash
install -d -m 700 ~/docker-log-viewer-server
cd ~/docker-log-viewer-server
```

이 디렉터리에는 인증 token과 TLS 개인 키가 저장됩니다. Git 저장소, 공유 폴더 또는 자동 백업 대상에 그대로 넣지 마세요.

## 3. 인증 token 생성

Agent와 브라우저는 서로 다른 token을 사용합니다.

```bash
umask 077
openssl rand -hex 32 > agent-token
openssl rand -hex 32 > web-token
chmod 600 agent-token web-token
```

- `agent-token`: 모든 Agent가 Server에 연결할 때 사용하는 공유 secret
- `web-token`: 브라우저 로그인 비밀번호
- 브라우저 사용자 이름의 기본값: `admin`

이미 연결된 Agent가 있다면 `agent-token`을 임의로 다시 생성하지 마세요. Token을 교체하면 모든 Agent의 token도 함께 변경해야 합니다.

## 4. 내부망용 TLS 인증서 생성

X.509 인증서에는 반드시 만료 시각이 있어 진정한 무기한 인증서는 만들 수 없습니다. 내부망에서는 장기 인증서를 사용할 수 있으며, 다음 예시는 10년 동안 유효한 self-signed 인증서를 생성합니다.

먼저 실제 Server IP를 지정합니다.

```bash
SERVER_HOST=192.168.10.10
```

IP 주소로 접속할 경우 다음 명령을 실행합니다.

```bash
openssl req -x509 \
  -newkey rsa:3072 \
  -nodes \
  -sha256 \
  -days 3650 \
  -keyout server.key \
  -out server.crt \
  -subj "/CN=${SERVER_HOST}" \
  -addext "subjectAltName=IP:${SERVER_HOST}" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign" \
  -addext "extendedKeyUsage=serverAuth"

chmod 600 server.key
chmod 644 server.crt
```

DNS 이름으로 접속할 경우 SAN 부분만 `DNS:`로 바꿉니다.

```bash
SERVER_HOST=logs.internal.example

openssl req -x509 \
  -newkey rsa:3072 \
  -nodes \
  -sha256 \
  -days 3650 \
  -keyout server.key \
  -out server.crt \
  -subj "/CN=${SERVER_HOST}" \
  -addext "subjectAltName=DNS:${SERVER_HOST}" \
  -addext "basicConstraints=critical,CA:TRUE" \
  -addext "keyUsage=critical,digitalSignature,keyEncipherment,keyCertSign" \
  -addext "extendedKeyUsage=serverAuth"

chmod 600 server.key
chmod 644 server.crt
```

인증서의 주소와 만료일을 확인합니다.

```bash
openssl x509 -in server.crt -noout -subject -dates -ext subjectAltName
```

`server.key`는 Server 밖으로 복사하면 안 됩니다. Agent에는 공개 인증서인 `server.crt`만 전달합니다.

## 5. 환경 설정 작성

```bash
cat > .env <<'EOF'
DOCKER_LOG_VIEWER_UI_PORT=18080
DOCKER_LOG_VIEWER_AGENT_PORT=19080
DOCKER_LOG_VIEWER_WEB_USER=admin
EOF

chmod 600 .env
```

필요하면 UI와 Agent 포트, 브라우저 사용자 이름을 실제 환경에 맞게 바꾸세요. 인증서에 넣은 Server 주소는 Compose 설정값이 아니며, Agent의 `.env`에서 연결 주소와 TLS 검증 이름으로 사용합니다.

## 6. Server Compose 작성

`compose.yaml`을 생성합니다.

```yaml
name: docker-log-viewer

services:
  server:
    image: ghcr.io/east-true/docker-log-viewer:v0.1.0
    pull_policy: missing
    restart: unless-stopped
    init: true
    read_only: true
    cap_drop:
      - ALL
    cap_add:
      - DAC_READ_SEARCH
    pids_limit: 100
    security_opt:
      - no-new-privileges:true
    tmpfs:
      - /tmp:rw,noexec,nosuid,size=16m
    environment:
      DOCKER_LOG_VIEWER_WEB_USER: ${DOCKER_LOG_VIEWER_WEB_USER:-admin}
    secrets:
      - agent-token
      - web-token
      - server-cert
      - server-key
    command:
      - server
      - -listen
      - 0.0.0.0:8080
      - -agent-listen
      - 0.0.0.0:9080
      - -agent-token-file
      - /run/secrets/agent-token
      - -tls-cert
      - /run/secrets/server-cert
      - -tls-key
      - /run/secrets/server-key
      - -web-token-file
      - /run/secrets/web-token
      - -web-tls-cert
      - /run/secrets/server-cert
      - -web-tls-key
      - /run/secrets/server-key
    ports:
      - "0.0.0.0:${DOCKER_LOG_VIEWER_UI_PORT:-18080}:8080"
      - "0.0.0.0:${DOCKER_LOG_VIEWER_AGENT_PORT:-19080}:9080"
    healthcheck:
      test:
        [
          "CMD",
          "wget",
          "--no-check-certificate",
          "--quiet",
          "--spider",
          "https://127.0.0.1:8080/api/health",
        ]
      interval: 10s
      timeout: 3s
      retries: 3
      start_period: 5s

secrets:
  agent-token:
    file: ./agent-token
  web-token:
    file: ./web-token
  server-cert:
    file: ./server.crt
  server-key:
    file: ./server.key
```

Compose 구문을 검사합니다.

```bash
docker compose config --quiet
```

아무 메시지도 출력되지 않고 종료되면 정상입니다.

## 7. Server 활성화

이미지를 받고 Server를 시작합니다.

```bash
docker compose pull
docker compose up -d --wait
docker compose ps
```

정상이면 `docker-log-viewer-server-1`이 `healthy` 상태로 표시됩니다. `restart: unless-stopped`가 설정되어 있으므로 Docker Engine이 시작될 때 Server도 다시 시작됩니다.

로그를 확인합니다.

```bash
docker compose logs --tail 100 server
```

다음 두 메시지가 나타나야 합니다.

```text
Docker Log Viewer Server UI listening on https://0.0.0.0:8080
Docker Log Viewer Server accepting Agents on 0.0.0.0:9080
```

## 8. 연결 확인

Server 호스트에서 UI health endpoint를 확인합니다.

```bash
curl --fail --insecure https://127.0.0.1:18080/api/health
```

정상 응답은 다음과 같습니다.

```json
{"status":"ok"}
```

IP 인증서를 사용한 경우 Agent TLS 포트를 확인합니다.

```bash
openssl s_client \
  -connect 192.168.10.10:19080 \
  -CAfile server.crt \
  -verify_ip 192.168.10.10 \
  -verify_return_error \
  -brief \
  </dev/null
```

DNS 인증서를 사용했다면 다음 명령으로 확인합니다.

```bash
openssl s_client \
  -connect logs.internal.example:19080 \
  -CAfile server.crt \
  -verify_hostname logs.internal.example \
  -verify_return_error \
  -brief \
  </dev/null
```

출력에 `Verification: OK`와 `Protocol version: TLSv1.3`이 있으면 정상입니다.

## 9. 브라우저 로그인

브라우저에서 다음 주소를 엽니다.

```text
https://192.168.10.10:18080
```

로그인 정보:

- 사용자 이름: `.env`의 `DOCKER_LOG_VIEWER_WEB_USER`, 기본값 `admin`
- 비밀번호: `web-token` 파일 내용

비밀번호를 확인할 때는 화면 공유와 셸 기록 노출에 주의합니다.

```bash
cat web-token
```

Self-signed 인증서는 브라우저가 기본으로 신뢰하지 않습니다. 반복되는 인증서 경고를 없애려면 `server.crt`를 관리 대상 PC의 신뢰할 수 있는 루트 인증서 저장소에 추가하세요. 인터넷에 직접 공개하는 경우에는 self-signed 인증서 대신 조직 CA 또는 공인 인증서를 사용해야 합니다.

## 10. 원격 Agent에 전달할 파일

Agent 관리자에게 다음 두 파일만 안전한 경로로 전달합니다.

- `agent-token`
- `server.crt` — Agent에서는 일반적으로 `server-ca.crt`라는 이름으로 저장

예시:

```bash
scp agent-token server.crt agent-user@agent-host:~/
```

`agent-user`와 `agent-host`는 원격 Agent 호스트의 실제 SSH 계정과 주소로 바꾸세요.

다음 파일은 전달하지 않습니다.

- `server.key`
- `web-token`
- `.env`

원격 호스트의 설치는 [Agent 설치 및 활성화](./agent.md)를 따릅니다.

## 11. 일상적인 운영

상태 확인:

```bash
docker compose ps
```

실시간 로그:

```bash
docker compose logs -f server
```

재시작:

```bash
docker compose restart server
```

중지 후 다시 활성화:

```bash
docker compose down
docker compose up -d --wait
```

새 버전으로 업그레이드할 때는 `compose.yaml`의 이미지 tag를 변경한 후 실행합니다.

```bash
docker compose pull
docker compose up -d --wait
```

`latest`보다 `v0.1.0`처럼 명시적인 버전을 사용하는 것을 권장합니다.

## 12. Token과 인증서 교체

Agent token을 교체할 때는 다음 순서를 지킵니다.

1. 새 `agent-token`을 생성합니다.
2. 모든 원격 Agent에 새 token을 배포합니다.
3. Server와 Agent를 짧은 시간 안에 차례로 재시작합니다.
4. UI에서 모든 Agent가 다시 connected 상태인지 확인합니다.
5. 이전 token 사본을 폐기합니다.

인증서 만료일은 다음 명령으로 확인합니다.

```bash
openssl x509 -in server.crt -noout -enddate
```

인증서를 교체하면 모든 Agent의 `server-ca.crt`도 새 인증서로 교체해야 합니다.

## 문제 해결

### 브라우저에서 인증서 경고가 표시됨

Self-signed 인증서를 사용하는 정상적인 초기 동작입니다. `server.crt`를 접속 PC의 신뢰 저장소에 추가하거나 조직 CA가 서명한 인증서를 사용하세요.

### `connection refused`

다음을 확인합니다.

```bash
docker compose ps
ss -lnt | grep -E ':(18080|19080)\b'
```

Server가 실행 중이고 두 포트가 올바른 주소에 bind되어 있어야 합니다.

### Agent가 handshake 단계에서 거부됨

Server와 Agent의 `agent-token`이 같은지 확인합니다. `web-token`을 Agent에 사용하면 안 됩니다.

### `x509: certificate is valid for ...`

Agent가 연결하는 주소, 인증서 SAN, Agent의 `-tls-server-name`이 모두 같은 IP 또는 DNS 이름이어야 합니다.

### Server는 정상인데 컨테이너가 보이지 않음

Server는 Docker socket을 직접 읽지 않습니다. 최소 한 대의 Agent가 connected 상태여야 합니다. [Agent 설치 및 활성화](./agent.md)를 진행하세요.
