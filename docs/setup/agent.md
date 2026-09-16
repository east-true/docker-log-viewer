# Agent 설치 및 활성화

이 문서는 Docker 호스트에 Agent를 설치하고 중앙 Docker Log Viewer Server에 연결하는 과정을 설명합니다. 명령을 위에서부터 순서대로 실행하면 해당 호스트의 컨테이너, 이미지와 로그가 중앙 UI에 나타납니다.

Agent는 중앙 Server로 outbound 연결을 시작합니다. Agent 호스트에는 별도의 수신 포트를 열 필요가 없습니다.

## 1. 준비 사항

Agent를 설치할 호스트에 다음 항목이 필요합니다.

- Linux
- 실행 중인 Docker Engine
- Docker Compose v2 (`docker compose`)
- 중앙 Server의 Agent 포트로 연결 가능한 내부 network
- Server 관리자가 전달한 `agent-token`
- Server 관리자가 전달한 `server.crt` 또는 CA 인증서

Docker를 확인합니다.

```bash
docker version
docker compose version
```

이 문서에서는 다음 예시 값을 사용합니다.

| 항목          | 예시                        | 설명                                 |
| ------------- | --------------------------- | ------------------------------------ |
| Server 주소   | `192.168.10.10`             | 인증서 SAN에 포함된 IP 또는 DNS 이름 |
| Agent 포트    | `19080`                     | Server의 Agent TLS 포트              |
| Agent 이름    | `production-01`             | 중앙 UI에 표시할 호스트 이름         |
| 설치 디렉터리 | `~/docker-log-viewer-agent` | Compose와 secret을 보관할 위치       |

예시 값은 실제 환경에 맞게 바꾸세요.

## 2. Server에서 필요한 파일 받기

Server 관리자로부터 다음 파일을 받습니다.

- `agent-token`: Agent 연결용 공유 secret
- `server.crt`: Server TLS 인증서 또는 이를 발급한 CA 인증서

Server에서 `scp`로 전달하는 예시:

```bash
scp agent-token server.crt agent-user@agent-host:~/
```

`agent-user`와 `agent-host`는 Agent 호스트의 실제 SSH 계정과 주소로 바꾸세요.

Agent에는 다음 파일이 필요하지 않습니다.

- Server TLS 개인 키인 `server.key`
- 브라우저 로그인용 `web-token`

개인 키를 전달받았다면 사용하지 말고 Server 관리자에게 알리세요.

## 3. 설치 디렉터리 준비

Agent 호스트에서 실행합니다.

```bash
install -d -m 700 ~/docker-log-viewer-agent
cd ~/docker-log-viewer-agent

install -m 600 ~/agent-token ./agent-token
install -m 644 ~/server.crt ./server-ca.crt

rm ~/agent-token ~/server.crt
```

Token 길이가 최소 32자인지 값 자체를 출력하지 않고 확인합니다.

```bash
test "$(tr -d '\r\n' < agent-token | wc -c)" -ge 32 \
  && echo "agent token: OK" \
  || echo "agent token: too short"
```

인증서 주소와 만료일을 확인합니다.

```bash
openssl x509 \
  -in server-ca.crt \
  -noout \
  -subject \
  -dates \
  -ext subjectAltName
```

## 4. Server TLS 연결 확인

IP 주소로 연결하는 경우:

```bash
openssl s_client \
  -connect 192.168.10.10:19080 \
  -CAfile server-ca.crt \
  -verify_ip 192.168.10.10 \
  -verify_return_error \
  -brief \
  </dev/null
```

DNS 이름으로 연결하는 경우:

```bash
openssl s_client \
  -connect logs.internal.example:19080 \
  -CAfile server-ca.crt \
  -verify_hostname logs.internal.example \
  -verify_return_error \
  -brief \
  </dev/null
```

다음 항목이 출력되면 정상입니다.

```text
Protocol version: TLSv1.3
Verification: OK
```

이 단계에서 실패하면 Agent를 실행하기 전에 network, Server 주소, 포트와 인증서를 먼저 바로잡으세요. Agent 포트는 HTTP가 아닌 gRPC/TLS이므로 `curl`로 확인하지 않습니다.

## 5. Agent 환경 설정 작성

```bash
cat > .env <<'EOF'
DOCKER_LOG_VIEWER_SERVER_HOST=192.168.10.10
DOCKER_LOG_VIEWER_AGENT_PORT=19080
DOCKER_LOG_VIEWER_TLS_SERVER_NAME=192.168.10.10
DOCKER_LOG_VIEWER_AGENT_NAME=production-01
EOF

chmod 600 .env
```

각 값을 실제 환경에 맞게 바꿉니다.

- `DOCKER_LOG_VIEWER_SERVER_HOST`: Agent가 실제로 접속할 IP 또는 DNS 이름
- `DOCKER_LOG_VIEWER_AGENT_PORT`: Server의 Agent 포트
- `DOCKER_LOG_VIEWER_TLS_SERVER_NAME`: 인증서 SAN과 정확히 같은 IP 또는 DNS 이름
- `DOCKER_LOG_VIEWER_AGENT_NAME`: 중앙 UI에서 구분하기 쉬운 고유 이름

Agent가 여러 대라면 `production-01`, `production-02`처럼 서로 다른 이름을 권장합니다.

## 6. Agent Compose 작성

`compose.yaml`을 생성합니다.

```yaml
name: docker-log-viewer

services:
  agent:
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
    environment:
      DOCKER_LOG_VIEWER_AGENT_NAME: ${DOCKER_LOG_VIEWER_AGENT_NAME}
    secrets:
      - agent-token
      - server-ca
    command:
      - agent
      - -server
      - ${DOCKER_LOG_VIEWER_SERVER_HOST}:${DOCKER_LOG_VIEWER_AGENT_PORT:-19080}
      - -server-ca
      - /run/secrets/server-ca
      - -tls-server-name
      - ${DOCKER_LOG_VIEWER_TLS_SERVER_NAME}
      - -agent-token-file
      - /run/secrets/agent-token
      - -state-dir
      - /var/lib/docker-log-viewer
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - agent-state:/var/lib/docker-log-viewer

volumes:
  agent-state:

secrets:
  agent-token:
    file: ./agent-token
  server-ca:
    file: ./server-ca.crt
```

Compose 구문과 환경변수 치환을 확인합니다.

```bash
docker compose config --quiet
```

아무 메시지도 출력되지 않고 종료되면 정상입니다.

## 7. Agent 설치 및 활성화

이미지를 받고 Agent를 시작합니다.

```bash
docker compose pull
docker compose up -d
docker compose ps
```

정상적으로 시작되면 컨테이너 이름은 `docker-log-viewer-agent-1`입니다. `restart: unless-stopped`가 설정되어 있으므로 Docker Engine이 시작될 때 Agent도 다시 시작됩니다.

## 8. 연결 확인

Agent 로그를 확인합니다.

```bash
docker compose logs --tail 100 agent
```

정상 연결 메시지는 다음 형태입니다.

```text
Agent <ID> connected to 192.168.10.10:19080
```

실시간으로 확인하려면 다음 명령을 사용합니다.

```bash
docker compose logs -f agent
```

중앙 UI에서 다음 항목을 확인합니다.

1. Images 탭의 Host 열에 `.env`에서 지정한 Agent 이름이 표시됩니다.
2. Logs 탭의 컨테이너 목록에 같은 Host 제목이 표시됩니다.
3. 원격 호스트의 컨테이너를 선택하면 과거 로그와 실시간 로그가 표시됩니다.

Agent는 연결이 끊어지면 자동으로 재연결을 시도합니다. UI에는 연결이 복구될 때까지 `재연결 중` 상태가 표시됩니다.

## 9. Agent ID와 상태 보존

Agent는 `agent-state` Docker volume에 영속 ID를 저장합니다. 컨테이너를 재생성해도 같은 volume을 유지하면 중앙 Server에서 같은 Host로 인식됩니다.

다음 명령은 컨테이너와 network만 제거하고 Agent ID는 보존합니다.

```bash
docker compose down
```

다음 명령은 Agent ID까지 제거하므로 일반적인 재설치나 업그레이드에는 사용하지 마세요.

```bash
docker compose down --volumes
```

Volume을 삭제한 뒤 다시 시작하면 새로운 Agent ID가 생성됩니다.

## 10. 일상적인 운영

상태 확인:

```bash
docker compose ps
```

실시간 로그:

```bash
docker compose logs -f agent
```

재시작:

```bash
docker compose restart agent
```

설정 변경 적용:

```bash
docker compose up -d
```

새 버전으로 업그레이드할 때는 `compose.yaml`의 이미지 tag를 변경한 후 실행합니다.

```bash
docker compose pull
docker compose up -d
```

`latest`보다 `v0.1.0`처럼 명시적인 버전을 사용하는 것을 권장합니다.

## 11. Agent 중지와 제거

잠시 중지:

```bash
docker compose stop agent
```

다시 활성화:

```bash
docker compose start agent
```

컨테이너 제거, Agent ID 보존:

```bash
docker compose down
```

완전 제거:

```bash
docker compose down --volumes
rm -f agent-token server-ca.crt .env compose.yaml
```

마지막 명령은 복구할 필요가 없을 때만 사용하세요.

## 12. 바이너리로 실행하는 방법

Compose 대신 Linux 바이너리를 사용해야 하는 환경에서는 아키텍처를 확인합니다.

```bash
case "$(uname -m)" in
  x86_64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)"; exit 1 ;;
esac
```

바이너리를 내려받고 검증합니다.

```bash
VERSION=0.1.0

curl --fail --location --remote-name \
  "https://github.com/east-true/docker-log-viewer/releases/download/v${VERSION}/docker-log-viewer_${VERSION}_linux_${ARCH}.tar.gz"

curl --fail --location --remote-name \
  "https://github.com/east-true/docker-log-viewer/releases/download/v${VERSION}/checksums.txt"

sha256sum --ignore-missing --check checksums.txt
tar -xzf "docker-log-viewer_${VERSION}_linux_${ARCH}.tar.gz"

sudo install -m 0755 \
  "docker-log-viewer_${VERSION}_linux_${ARCH}/docker-log-viewer" \
  /usr/local/bin/docker-log-viewer

docker-log-viewer --version
```

실행 예시:

```bash
sudo install -d -o "$USER" -g "$(id -gn)" -m 700 /var/lib/docker-log-viewer

docker-log-viewer agent \
  -server 192.168.10.10:19080 \
  -server-ca "$HOME/docker-log-viewer-agent/server-ca.crt" \
  -tls-server-name 192.168.10.10 \
  -agent-token-file "$HOME/docker-log-viewer-agent/agent-token" \
  -state-dir /var/lib/docker-log-viewer \
  -name production-01
```

백그라운드 상시 실행이 필요하다면 Docker Compose 또는 운영체제의 systemd service로 등록하세요. 실행 계정은 `/var/run/docker.sock`에 접근할 수 있어야 합니다.

## 문제 해결

### `connection refused`

- Server 주소와 Agent 포트가 맞는지 확인합니다.
- Server의 Agent listener가 `127.0.0.1`이 아니라 내부망에서 접근 가능한 주소에 bind되어 있어야 합니다.
- Agent 호스트에서 Server까지 network route가 있는지 확인합니다.

```bash
nc -vz 192.168.10.10 19080
```

### `x509: certificate signed by unknown authority`

Server가 제공한 올바른 `server.crt` 또는 CA 인증서를 `server-ca.crt`로 설치했는지 확인합니다.

### `x509: certificate is valid for ..., not ...`

다음 세 값이 일치해야 합니다.

- Agent가 접속하는 Server IP 또는 DNS 이름
- 인증서의 Subject Alternative Name
- `.env`의 `DOCKER_LOG_VIEWER_TLS_SERVER_NAME`

### Agent handshake가 거부됨

`agent-token`이 Server의 token과 정확히 같은지 확인합니다. 줄 끝 공백을 추가하거나 `web-token`을 대신 사용하면 안 됩니다.

### `permission denied` 또는 Docker socket 연결 실패

Docker Engine이 실행 중이고 socket이 존재하는지 확인합니다.

```bash
docker version
ls -l /var/run/docker.sock
```

Compose 구성에는 Docker socket이 다음과 같이 read-only로 마운트되어 있어야 합니다.

```yaml
- /var/run/docker.sock:/var/run/docker.sock:ro
```

### Agent는 connected인데 컨테이너가 보이지 않음

```bash
docker ps -a
docker compose logs --tail 200 agent
```

Agent가 바라보는 Docker Engine에 컨테이너가 있는지 확인합니다. Rootless Docker를 사용한다면 실제 socket 경로와 `DOCKER_HOST` 설정이 추가로 필요할 수 있습니다.
