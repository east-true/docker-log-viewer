<p align="center">
  <img src="internal/web/assets/favicon.svg" width="88" alt="Docker Log Viewer logo">
</p>

<h1 align="center">Docker Log Viewer</h1>

<p align="center">
  한 대의 Docker 호스트에서 모든 컨테이너를 찾고,<br>
  선택한 컨테이너의 과거·실시간 로그를 브라우저로 확인하는 읽기 전용 도구입니다.
</p>

<p align="center">
  <a href="https://github.com/east-true/docker-log-viewer/actions/workflows/ci.yml"><img src="https://github.com/east-true/docker-log-viewer/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white" alt="Go 1.25">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="Apache 2.0 license"></a>
  <img src="https://img.shields.io/badge/protected%20by-Gitleaks-1679b8" alt="Protected by Gitleaks">
</p>

<p align="center">
  <a href="#빠른-시작">빠른 시작</a> ·
  <a href="#주요-기능">주요 기능</a> ·
  <a href="#보안">보안</a> ·
  <a href="#개발과-테스트">개발과 테스트</a>
</p>

![Docker Log Viewer에서 컨테이너의 실시간 로그를 확인하는 화면](docs/assets/overview.png)

<p align="center"><sub>문서용 합성 데이터로 만든 화면입니다. 실제 로컬 컨테이너나 이미지 정보는 포함하지 않습니다.</sub></p>

## 왜 Docker Log Viewer인가요?

로그 한 줄을 확인하기 위해 컨테이너 관리 플랫폼 전체가 필요하지는 않습니다. Docker Log Viewer는 단일 호스트의 로그 확인에 필요한 기능만 제공합니다.

| 원칙 | 동작 |
|---|---|
| 집중된 화면 | 모든 컨테이너를 조회한 뒤 하나를 선택해 로그를 확인합니다. |
| 읽기 전용 | 컨테이너 시작·중지·삭제 또는 Docker 설정 변경 API가 없습니다. |
| 저장하지 않음 | 서버는 로그를 수집하거나 보관하지 않습니다. 브라우저 메모리에 최근 10,000줄만 유지합니다. |
| 작은 배포 단위 | Go 서버와 웹 UI가 하나의 바이너리에 포함됩니다. 별도 데이터베이스가 없습니다. |

## 빠른 시작

### Docker Compose

Linux와 Docker Compose v2가 필요합니다.

```bash
git clone https://github.com/east-true/docker-log-viewer.git
cd docker-log-viewer
docker compose up --build -d
```

브라우저에서 <http://127.0.0.1:8080>을 여세요.

```bash
# 종료
docker compose down
```

### 소스에서 실행

Docker Engine에 접근할 수 있는 Linux 환경과 Go 1.25 이상이 필요합니다.

```bash
go run ./cmd/docker-log-viewer
```

기본 Docker 소켓 외의 Engine을 사용한다면 Docker 표준 환경 변수 `DOCKER_HOST`, `DOCKER_TLS_VERIFY`, `DOCKER_CERT_PATH`를 사용할 수 있습니다.

## 주요 기능

### Logs

- 실행 중이거나 중지된 모든 컨테이너 조회 및 검색
- 선택한 컨테이너의 최근 로그와 실시간 로그 스트리밍
- 100~5,000줄, 전체 tail 또는 최근 5분~7일 범위 선택
- 날짜와 밀리초가 포함된 로컬 시간 표시
- 대소문자를 구분하지 않는 현재 버퍼 검색
- stderr 숨김, 긴 줄 줄바꿈, 화면 비우기 및 파일 저장
- 실시간 ON/OFF 전환 시 기존 로그와 스크롤 문맥 유지
- 연결 복구 시 마지막 Docker 타임스탬프부터 재개하고 경계 로그 중복 제거
- 위로 스크롤한 동안 들어온 새 로그 개수와 최신 위치 이동 버튼 표시

### Images

- 로컬 이미지 전체 조회 및 검색
- 이미지 크기와 생성 시점 표시
- 각 이미지를 사용하는 컨테이너와 실행 상태 표시
- 전체 이미지, 사용 중 이미지, 실행 컨테이너가 연결된 이미지 집계

### 키보드

| 키 | 이동 |
|---|---|
| <kbd>I</kbd> | Images |
| <kbd>L</kbd> | Logs |

입력 필드에 포커스가 있을 때는 단축키가 동작하지 않습니다.

## 동작 방식

```text
Browser
  ├─ GET /api/containers, /api/images
  └─ NDJSON /api/logs
          │
          ▼
  Docker Log Viewer
  (single Go binary)
          │ read-only calls
          ▼
     Docker Engine
```

컨테이너와 이미지 목록은 5초마다 갱신됩니다. 로그는 선택한 컨테이너에 대해서만 스트리밍되며, 탭이나 브라우저를 닫으면 연결도 종료됩니다.

## 범위와 한계

Docker Log Viewer가 잘 맞는 경우:

- 개발 PC나 단일 Linux 서버의 컨테이너 로그를 빠르게 확인할 때
- Docker CLI를 반복 입력하지 않고 과거 로그와 실시간 로그를 오갈 때
- 컨테이너 조작 기능이 없는 읽기 전용 화면이 필요할 때

다음 용도로는 설계되지 않았습니다:

- 여러 Docker 호스트의 중앙 집중식 로그 수집
- 장기 보관, 인덱싱, 정규식·SQL 기반 로그 분석
- 사용자 계정, 역할 기반 접근 제어 또는 감사 로그
- Compose, 네트워크, 볼륨 및 컨테이너 수명 주기 관리
- Kubernetes 또는 Docker Swarm 관제

이런 요구사항에는 Loki, Elasticsearch 또는 전용 로그 수집 플랫폼이 더 적합합니다.

## 설정

| 설정 | 기본값 | 설명 |
|---|---|---|
| `-listen` | `127.0.0.1:8080` | HTTP 수신 주소 |
| `DOCKER_LOG_VIEWER_LISTEN` | `127.0.0.1:8080` | 플래그를 지정하지 않았을 때의 수신 주소 |
| `DOCKER_HOST` | Docker 기본값 | Docker Engine endpoint |
| `DOCKER_TLS_VERIFY` | Docker 기본값 | 원격 Engine TLS 검증 |
| `DOCKER_CERT_PATH` | Docker 기본값 | 원격 Engine 인증서 경로 |

예를 들어 사설망 인터페이스에서 수신하려면 다음과 같이 실행할 수 있습니다.

```bash
docker-log-viewer -listen 0.0.0.0:8080
```

## 보안

> [!WARNING]
> Docker Log Viewer에는 인증 기능이 없습니다. 기본값처럼 loopback에 바인딩하거나, 신뢰할 수 있는 사설망의 인증 프록시 뒤에서만 사용하세요.

- 컨테이너 로그에는 토큰, 비밀번호, 개인정보가 포함될 수 있습니다.
- `/var/run/docker.sock`을 읽기 전용 파일로 마운트해도 Docker Engine API 자체의 강한 권한은 줄어들지 않습니다.
- 서버 API는 GET 요청과 Docker 조회 API만 구현하지만, 신뢰하지 않는 이미지나 공개 네트워크에서 실행하면 안 됩니다.
- 브라우저에서 저장한 로그 파일은 사용자가 직접 보호하고 삭제해야 합니다.

Compose 기본 설정은 `127.0.0.1:8080`에만 포트를 공개하고, 컨테이너 루트 파일 시스템을 읽기 전용으로 실행하며 `no-new-privileges`를 적용합니다.

## HTTP API

모든 API는 읽기 전용이며 응답 캐싱을 비활성화합니다.

| 경로 | 응답 |
|---|---|
| `GET /api/health` | HTTP 서버 상태 |
| `GET /api/containers` | 중지된 항목을 포함한 전체 컨테이너 |
| `GET /api/images` | 이미지와 연결된 컨테이너 상태 |
| `GET /api/logs` | 선택한 컨테이너의 NDJSON 로그 스트림 |

```text
/api/logs?container=<64-character-container-id>&tail=200&since=1h&follow=true
```

| 파라미터 | 값 |
|---|---|
| `container` | 필수, 전체 64자리 컨테이너 ID |
| `tail` | `1`~`10000` 또는 `all`, 기본값 `200` |
| `since` | `5m`, `15m`, `1h`, `6h`, `24h`, `7d` 또는 RFC3339 타임스탬프 |
| `follow` | `true` 또는 `false`, 기본값 `true` |

## 문제 해결

### Docker Engine에 연결할 수 없음

Docker가 실행 중인지 확인하고, 현재 사용자 또는 애플리케이션 프로세스가 Docker 소켓에 접근할 수 있는지 확인하세요.

```bash
docker info
ls -l /var/run/docker.sock
```

### 컨테이너는 보이지만 로그가 없음

해당 컨테이너에서 `docker logs <container>`가 출력되는지 먼저 확인하세요. Docker Engine에서 제공하지 않는 로그는 이 애플리케이션에서도 표시할 수 없습니다.

### 8080 포트가 이미 사용 중

```bash
go run ./cmd/docker-log-viewer -listen 127.0.0.1:18080
```

## 개발과 테스트

### 백엔드

```bash
gofmt -w ./cmd ./internal
go vet ./...
go test -race ./...
go build ./cmd/docker-log-viewer
```

### 웹 UI

먼저 한 터미널에서 애플리케이션을 실행합니다.

```bash
go run ./cmd/docker-log-viewer -listen 127.0.0.1:18080
```

다른 터미널에서 테스트를 실행합니다.

```bash
npm ci
npx playwright install chromium
npm test
```

README 스크린샷은 실제 Docker 정보를 사용하지 않고 합성 API fixture로 다시 만들 수 있습니다.

```bash
npm run docs:screenshot
```

GitHub Actions CI는 모든 push와 pull request에서 다음을 검사합니다.

- Go 포맷, `go vet`, race detector 테스트 및 바이너리 빌드
- Docker 이미지 빌드
- JavaScript 단위 테스트와 Chromium UI 테스트
- 실제 임시 컨테이너를 사용한 실시간 로그 중지·재개
- Gitleaks를 사용한 전체 Git 이력의 민감정보 검사

로컬 Gitleaks 검사:

```bash
docker run --rm -v "$PWD:/repo:ro" ghcr.io/gitleaks/gitleaks:v8.30.1 git --redact --verbose /repo
```

## 릴리즈

시맨틱 버전 태그를 푸시하면 CI가 모두 성공한 뒤 GitHub Release와 릴리즈 노트가 자동 생성됩니다.

```bash
git tag v0.1.0
git push origin v0.1.0
```

`v0.1.0-rc.1` 같은 태그는 사전 릴리즈로 생성됩니다. 릴리즈 노트는 pull request 라벨에 따라 새로운 기능, 버그 수정, 접근성, 문서 및 기타 변경으로 분류됩니다.

## 프로젝트 구조

```text
cmd/docker-log-viewer/       실행 진입점
internal/dockerengine/      Docker Engine 읽기 전용 어댑터
internal/web/               HTTP API와 임베디드 웹 UI
scripts/                    문서용 합성 스크린샷 도구
tests/ui/                   브라우저 및 로그 조립기 테스트
```

## 기여하기

버그와 기능 제안은 [GitHub Issues](https://github.com/east-true/docker-log-viewer/issues)에 등록해 주세요. 변경 사항은 작은 단위로 테스트와 함께 pull request로 제출하는 것을 권장합니다.

보안 취약점이나 실제 secret은 공개 이슈에 올리지 마세요.

## 라이선스

[Apache License 2.0](LICENSE)
