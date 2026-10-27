# 격리 CLI 통합 테스트

`scripts/integration-test.py`는 미리 빌드한 **정적 Linux 바이너리**와 공식 etcd
v3.7.3을 연결하는 독립 실행 테스트다. Go 빌드, 모듈 변경, 패키지 설치,
운영 endpoint 접근을 하지 않는다. 제품의 런타임 의존성이 아니라 개발·CI 도구다.

## 준비 및 실행

Linux(Ubuntu 24.04 CI 포함), Python 3 표준 라이브러리, OpenSSL, **rootless
Podman**이 필요하다. rootful Podman은 사전 점검에서 거부한다. 실행 사용자의
user namespace/subuid/subgid 및 rootless 컨테이너 실행 환경은 미리 준비해야 한다.
이 스크립트는 호스트 설정을 바꾸거나 필요한 프로그램을 설치하지 않는다.

공식 이미지를 미리 준비한다. 아래 pull은 네트워크를 사용하는 명시적인 준비
단계이며, 스크립트 자체는 pull을 하지 않는다.

```sh
podman pull gcr.io/etcd-development/etcd:v3.7.3
podman info --format '{{.Host.Security.Rootless}}'  # true
```

모든 컨테이너 생성은 `--pull=never --network=none`을 사용한다. 이미지가 캐시에
없으면 실패한다. 테스트 기록에는 실제 캐시 이미지 ID를 남긴다. 이미지 내부의
shell, sleep, find 또는 기본 ENTRYPOINT에 의존하지 않고 `/usr/local/bin/etcd`,
`etcdctl`, `etcdutl` 또는 전달한 제품 바이너리를 명시적으로 실행한다.

전체 제품 테스트의 표준 호출:

```sh
python3 scripts/integration-test.py \
  --binary /absolute/path/to/static/etcd-backup-keeper \
  --evidence-dir /tmp/etcd-keeper-integration-final-unique
```

`--binary`는 호출자가 별도로 준비한 정적 ELF 실행 파일이어야 한다. 스크립트는
ELF의 동적 인터프리터(PT_INTERP)가 있는 파일을 거부하고, private 임시 디렉터리에
복사한 파일만 컨테이너에서 실행한다. 호스트 바이너리 자체를 relabel하지 않는다.
호스트/이미지와 바이너리 아키텍처가 일치해야 한다. CI도 전체 호출을 그대로
사용하고 fixture-only 옵션을 넣지 않는다.

`--evidence-dir`는 **체크아웃 밖의 아직 없는 디렉터리**여야 한다. 생략하면
`/tmp/etcd-keeper-integration-evidence-*`를 만든다. `integration.log`는 기존 파일을
덮어쓰지 않으며, 시나리오별 PASS/NOT RUN, 최종 결과, 이미지 ID, 전체 모드의
바이너리 SHA-256을 기록한다. 같은 디렉터리의 `summary.json`에도 모드, 성공/정리
여부, 이미지 ID, 바이너리 해시, 시나리오 결과와 미구현 항목을 기록한다. 이 두
파일만 생성하며 fixture는 증거 디렉터리에 넣지 않는다. 종료 코드 0은 선택한
모드의 구현된 검사들이 성공하고 정리가 완료됐다는 뜻이다. CI artifact allowlist는
`integration.log`와 `summary.json` 두 파일이다. credential/backup 임시 디렉터리는
수집하지 않는다.

제품 바이너리 없이 공식 도구의 fixture 준비만 검증할 때:

```sh
python3 scripts/integration-test.py --fixtures-only \
  --evidence-dir /tmp/etcd-keeper-integration-fixtures-unique
```

`--fixture-only`도 호환 별칭이다. 이 모드는 공식 snapshot/restore 및 TLS/mTLS와
비밀번호 인증 fixture를 검사할 뿐이다. **제품 backup/status/verify/version
인수 검증이 아니며**, 미실행 제품 검사는 각각 NOT RUN으로 표시한다.

## 전체 모드의 검사 범위

- 격리 etcd에 두 key/value 쓰기 → 제품 backup → 모든 완료 백업 및 이름 지정
  verify → 공식 etcdutl로 **다른 데이터 디렉터리** 복원 → 별도 컨테이너의 별도
  client/peer 포트(32379/32380)에서 동일한 값 읽기.
- TLS 서버 검증, mTLS, private `password_file`을 사용하는 비밀번호 인증.
  테스트 전용 CA/인증서/비밀번호는 실행마다 새로 만든다.
- 첫 endpoint가 연결 거부일 때 두 번째 후보로 성공. 별도 프로세스·데이터·포트로
  시작한 서로 다른 클러스터 후보는 실패하고 이전 완료 백업 바이트를 보존해야 한다.
- `keep=2`로 반복 백업 후 정확히 두 개의 private 완료 디렉터리를 유지하고 새
  백업을 보존. 각 회차에 로컬 verify도 성공해야 한다.
- snapshot 한 바이트를 같은 크기로 바꿨을 때 status는 성공하지만 verify는 실패.
  원본을 `finally`로 복구한 뒤 다시 verify 성공. metadata/manifest 손상도 각각
  거부하고 fixture를 복구한다.
- 가용 공간보다 큰 `min_free_bytes`, 도달 불가 endpoint, 잘못된 CA/비밀번호는
  실패하며 기존 완료 백업 집합과 각 파일 해시를 보존한다.
- 도달 불가 endpoint의 긴 연결 점검 중 실제 CLI가 저장소 잠금을 획득했는지
  확인한 뒤 컨테이너 PID 1에 SIGTERM을 보낸다. 취소 종료 코드 130과 `canceled`
  JSON 오류, 기존 완료 백업의 바이트 보존을 확인한다. 짧은 스냅샷의 시간 경쟁에
  의존하지 않는다.
- 원격 etcd 컨테이너를 제거하고 인증 fixture 디렉터리까지 삭제한 뒤, 새로 시작한
  격리 컨테이너에서 오프라인 status/verify(전체 및 이름 지정)가 성공해야 한다.
  빈 백업 디렉터리에서 verify는 실패해야 한다.

제품 JSON은 단일 object의 `ok`, `command`, 실패 시 `error`만 해석한다. 작업별
`result` 내부 필드는 가정하지 않는다. 저장 파일 검사는 문서화된
`backup-*/{snapshot.db,metadata.json,SHA256SUMS}` 계약을 사용한다.

## 격리, 비밀 보호, 한계

컨테이너는 무작위 이름으로 만들고 host network, published port, privileged
옵션을 쓰지 않는다. 모든 endpoint는 컨테이너 내부 loopback이다. 각 subprocess에
시간 제한이 있고 readiness도 제한된 시간만 재시도한다. 예외·SIGINT·SIGTERM 시
`finally`에서 **이 실행이 소유한 정확한 컨테이너 이름만** 제거한 뒤 private
임시 fixture를 삭제한다. 정리 실패는 실패 결과와 해당 이름/경로를 남긴다.
SIGKILL·호스트 장애는 정리를 보장할 수 없으므로 출력의 정확한 소유 리소스를
확인해 수동 정리한다. 광범위한 `podman prune`은 사용하지 않는다.

비밀번호와 개인키는 disposable 테스트 전용이며 실제 자격 증명을 받지 않는다.
제품 비밀번호는 argv가 아니라 0600 파일로만 전달한다. 공식 도구의 사용자
생성은 stdin, 인증 점검은 private env-file 경로로 전달한다. 자식 프로세스 출력에
생성된 비밀번호/개인키가 포함됐는지 검사하고, 원문 stdout/stderr와 키·비밀번호를
증거에 출력하지 않는다. 오류 진단은 의도적으로 일반화되어 있다.

연결 점검 중 실제 SIGTERM 취소는 전체 시나리오에 포함한다. 결정적인 스냅샷
스트림 중간 네트워크 단절 주입은 통합 harness에 없으며 **전체 실행에서도 NOT
RUN으로 명시한다**. 원격 스트림 실패/재시도와 기존 백업 보존은 엔진 단위 테스트가
별도로 다룬다. 실패한 실행은 남은 검사도 NOT RUN으로 표시하고 성공을
가장하지 않는다. 이 harness는 단위 테스트, 위험한 symlink/정리 경계, 실제 운영
스토리지 내구성, 패키지 수명주기 및 전체 보안 검토를 대체하지 않는다. 특히 공식
서버의 Snapshot 응답 Header는 채워지지 않으므로 복원 성공만으로 스트림 자체의
클러스터 신원이 증명되는 것은 아니다. 제품의 전·후 Status 및 endpoint 신뢰
검사와 별개인 복원 증거다.

## 검증 상태

최종 제품 바이너리로 전체 18개 시나리오와 fixture 정리를 통과했다. 구체적인
구현·검증 매핑과 바이너리 해시는 [요구사항 기록](../docs/requirements.md)을 참고한다.
이후 CI/개발 실행도 `integration.log`, `summary.json`, 종료 코드와 해당 바이너리의
SHA-256을 연결해 판단한다. 과거 실행 성공이 변경된 바이너리의 성공을 대신하지 않는다.
