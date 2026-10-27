# 빠른 시작

릴리스 패키지를 설치해 첫 수동 백업을 확인하는 운영자용 절차입니다.
기본 예시는 **kubeadm stacked etcd가 실행 중인 control-plane 노드**에서 root로
접근하는 경우입니다. 운영 대상·백업 부하·보관 정책을 승인하고, 먼저 승인된
비운영 환경에서 백업·[격리 복원 훈련](restore-drill.md)을 확인하세요.
외부 etcd나 별도 백업 호스트는 이 loopback·인증서 예시를 그대로 사용할 수 없습니다.

## 1. 릴리스 패키지 설치

[공개 릴리스 v20261027-3](https://github.com/spi-ca/etcd-backup-keeper/releases/tag/v20261027-3)에서
OS에 맞는 파일과 `SHA256SUMS`를 같은 다운로드 디렉터리에 받습니다.
아래 예시는 이 릴리스 기준이며, 다른 버전을 설치한다면 파일 이름과 지원 범위도 확인하세요.

**이미 설치되어 있다면 재설치·업그레이드 전에**
[중지·완료 대기·설정과 백업 보존 절차](operations.md#5-systemd-스케줄러와-패키지-수명주기)를
먼저 따르세요. 설치 명령은 기존 바이너리를 교체할 수 있으며 실행 중 작업을 자동 중지하지 않습니다.

다운로드 디렉터리에서 **아래 한 가지 OS의 명령만** 실행합니다. 체크섬 검사가
성공해야 설치되며, 선택한 패키지 파일이 실제로 검사되어 `OK`로 표시되어야 합니다.
`--ignore-missing`은 다운로드하지 않은 다른 OS의 배포물만 건너뛰기 위한 옵션입니다.

```sh
# Ubuntu 24.04 amd64:
sha256sum --ignore-missing --check SHA256SUMS &&
  sudo apt install ./etcd-backup-keeper_20261027-3_amd64.deb
# Arch Linux x86_64: 위 두 줄 대신 다음 두 줄의 주석을 해제해 실행합니다.
# sha256sum --ignore-missing --check SHA256SUMS &&
#   sudo pacman -U ./etcd-backup-keeper-20261027-3-x86_64.pkg.tar.zst
etcd-backup-keeper version
etcd-backup-keeper --help
```

해시가 일치하지 않거나 선택한 파일이 검사되지 않았으면 설치하지 마세요. 해시 파일도
같은 신뢰할 수 있는 릴리스 경로에서 받아야 하며, 해시 일치만으로 게시자의 진위를
증명하지는 않습니다. CLI 런타임에 Go·호스트 `etcdctl`·`etcdutl`은 필요하지 않습니다.
패키지는 systemd에 의존하며 service/timer를 **설치만 하고 활성화·시작하지 않습니다.**

정적 `.tar.gz`는 별도 수동 배치용이며 패키지의 설정·unit 설치를 대신하지 않습니다.
개발용 소스 빌드와 패키지 제작은 [요구사항](requirements.md#소스-빌드)·
[패키징 안내](../packaging/README.md)로 분리합니다.

## 2. 설치된 private 설정을 편집

패키지가 설치한 `/etc/etcd-backup-keeper/config.toml`을 **직접 검토·편집**합니다.
기존 운영 설정 위에 `config.example.toml`을 복사하거나 아래 예시 전체로 덮어쓰지
마세요. 재설치·업그레이드라면 먼저 [중지·보존 절차](operations.md#5-systemd-스케줄러와-패키지-수명주기)를 따릅니다.

```sh
sudo test -f /etc/etcd-backup-keeper/config.toml
sudo test ! -L /etc/etcd-backup-keeper/config.toml
sudo stat -c '%a %U:%G %n' /etc/etcd-backup-keeper /etc/etcd-backup-keeper/config.toml
sudoedit /etc/etcd-backup-keeper/config.toml
```

파일이 없거나 symlink이거나 소유권·권한이 예상과 다르면 편집 전에 조사합니다.
기본 root 실행에서는 설정 디렉터리 `root:root 0700`, 설정 파일 `root:root 0600`을
유지합니다. 인증 자료·백업도 root 실행과 timer에서 동일하게 읽을 수 있어야 합니다.
설정에 비밀번호나 개인키 내용을 넣지 않습니다.

### 기본 예시: kubeadm stacked etcd control-plane

일반적인 kubeadm 경로를 실제 노드의 etcd 설정과 대조한 뒤, 기존 설정의 해당 키를
수정하거나 주석을 해제합니다. **같은 키를 중복 추가하지 마세요.** 예시는 새로운
기본 설정이 아니라 운영자가 선택할 값입니다.

```toml
endpoints = ["https://127.0.0.1:2379"]
backup_dir = "/var/lib/etcd-backup-keeper"
keep = 7
min_free_bytes = 1073741824
ca_file = "/etc/kubernetes/pki/etcd/ca.crt"
cert_file = "/etc/kubernetes/pki/etcd/healthcheck-client.crt"
key_file = "/etc/kubernetes/pki/etcd/healthcheck-client.key"
# expected_cluster_id = "실제조회한16진수ID"
```

`expected_cluster_id`는 선택 사항입니다. 위 설명용 문자열을 활성화하지 말고 다음
절차에서 확인한 **0이 아닌 64-bit 16진수**만 사용하세요. 나머지 timeout 등은
설치된 설정에서 검토하며 전체 키 계약은 [운영 안내](operations.md#설정-계약)가 기준입니다.

- `ca_file`은 **공개 CA 인증서 `ca.crt`**입니다. `ca.key`는 필요하지 않습니다.
  **CA 개인키, `server.key`, `peer.key`를 백업 client 인증용으로 쓰거나 복사하지 마세요.**
- `healthcheck-client.crt`/`.key`는 위 stacked etcd 예시의 client 인증 쌍입니다.
  실제 서버의 client CA 신뢰·인증 정책·유효기간·필요한 snapshot 권한에 맞아야 합니다.
  사용자 인증도 필요한 환경은 [운영 안내의 비밀번호 파일 계약](operations.md#tls와-비밀번호)을 따릅니다.
- 서버 인증서의 SAN에 endpoint의 IP 또는 DNS 이름이 있어야 합니다. `127.0.0.1`이
  SAN에 없으면 실제 인증서와 일치하는 승인된 endpoint를 사용하세요. TLS 검증은 끄지 않습니다.
- 개인키는 실행 UID 소유의 private 일반 파일이며 group/other 권한이 없어야 합니다
  (`0600` 권장). 마지막 경로 symlink는 거부합니다. 상위 경로도 운영자가 통제해야 합니다.
  kubeadm이 관리하는 키의 권한을 임의로 바꾸거나 개인키를 출력하지 마세요. 조건이
  맞지 않으면 승인된 방식으로 전용 client 인증 쌍을 준비합니다.
- **외부 etcd / 원격 백업 호스트**에서는 loopback 대신 실제 도달 가능한 endpoint,
  해당 서버 SAN·CA·인증 정책에 맞는 별도 client 인증서와 키가 필요합니다. control-plane
  키를 무조건 복사하지 마세요. systemd의 `ProtectHome=true` 때문에 `/home` 아래 자료는
  사용할 수 없으며, 다른 백업 경로는 [unit 쓰기 경로](operations.md#5-systemd-스케줄러와-패키지-수명주기)도 조정해야 합니다.

### 선택 사항: 실제 cluster ID를 확인해 pin 설정

이 단계는 Kubernetes API와 etcd에 **읽기 전용 접속**합니다. 승인된 kubeadm
control-plane에서 `kubectl`, 관리자 kubeconfig, 호스트의 Python 3가 있을 때만
실행합니다. 제품 CLI의 런타임 의존성을 추가하는 단계가 아닙니다.

```sh
sudo kubectl --kubeconfig=/etc/kubernetes/admin.conf -n kube-system \
  get pods -l component=etcd -o wide
# 위 출력에서 의도한 노드의 실제 Pod 이름으로 바꿉니다.
ETCD_POD='etcd-실제노드이름'
```

다음은 **Bash**에서 실행합니다. `etcdctl`은 실제 etcd 컨테이너 안의 것을 쓰므로
호스트에 설치할 필요가 없습니다. Python은 **Pod 밖 호스트**에서 실행하며 JSON의
10진 정수를 손실 없이 읽어 TOML용 16진수로 출력합니다. `-it`는 사용하지 않습니다.

```bash
set -o pipefail
sudo kubectl --kubeconfig=/etc/kubernetes/admin.conf -n kube-system \
  exec "$ETCD_POD" -c etcd -- etcdctl \
  --endpoints=https://127.0.0.1:2379 \
  --cacert=/etc/kubernetes/pki/etcd/ca.crt \
  --cert=/etc/kubernetes/pki/etcd/healthcheck-client.crt \
  --key=/etc/kubernetes/pki/etcd/healthcheck-client.key \
  endpoint status -w json | python3 -c '
import json, sys
rows = json.load(sys.stdin)
ids = [row["Status"]["header"]["cluster_id"] for row in rows]
if not ids or any(type(n) is not int or not 0 < n < 2**64 for n in ids):
    raise SystemExit("invalid uint64 cluster_id")
if len(set(ids)) != 1:
    raise SystemExit("cluster IDs differ")
print(format(ids[0], "x"))
'
```

명령이 성공했을 때만 결과를 사용합니다. `jq` 등 부동소수점으로 변환하는 경로에서
64-bit ID를 반올림하지 마세요. `expected_cluster_id = "출력된16진수"`로 설정을
편집하고 의도한 클러스터와 대조합니다. ID는 인증 정보나 암호학적 출처 증명이 아닙니다.
기존 백업의 cluster pin과 달라도 거부하므로 [불일치 조사](troubleshooting.md#cluster_mismatch)가 필요합니다.

`kubectl`이 `http://localhost:8080`으로 접속하려 하면 API server나 etcd endpoint를
바꾸지 말고 [kubeconfig 확인](troubleshooting.md#kubectl과-localhost8080)을 진행하세요.
외부 etcd나 별도 Pod·인증서 배치에서는 이 명령을 그대로 쓰지 말고 실제 구성에 맞춥니다.

## 3. 오프라인 점검 → 접속은 명시적으로 선택

```sh
sudo etcd-backup-keeper check --config /etc/etcd-backup-keeper/config.toml
# 대상 접속을 의도한 경우에만:
sudo etcd-backup-keeper check --config /etc/etcd-backup-keeper/config.toml --connect
```

`check`의 기본은 완전 오프라인입니다. 설정과 명시한 인증 자료를 읽고 저장 경로·권한을
제한적으로 확인하지만 저장소나 `.lock`을 생성·수정하거나 쓰지 않습니다. 마지막 백업
디렉터리가 미생성이면 `unverified`이며 자동 생성하지 않습니다. 상위 경로는 미리
존재하고 안전한 권한으로 관리되어야 합니다.

`--connect`는 각 후보에 Status만 요청합니다. 후보 하나라도 실패하면 전체가 실패하며
실제 backup failover와 다릅니다. `confirmed`는 해당 제한 안에서 확인, `unverified`는
미확인, `failed`는 관측된 실패입니다. 종료 `0`도 쓰기·잠금·fsync·내구성·snapshot 권한·
기존 백업 pin·무결성·복원 성공 보증이 아닙니다. 상세 범위는
[check 계약](operations.md#선택적-읽기-전용-점검-check)을 참고하세요.

## 4. 수동 백업·목록·검증

대상·부하·저장소를 확인한 뒤 승인된 환경에서 실행합니다. 기본 백업 루트는 `/var/lib`까지
존재하면 CLI가 마지막 디렉터리를 생성합니다. 기존 루트는 실행 UID 소유의 정확한
`0700`이어야 합니다. systemd와 같은 root·설정·경로로 수동 확인합니다.

```sh
sudo etcd-backup-keeper backup --config /etc/etcd-backup-keeper/config.toml --verbose
sudo etcd-backup-keeper status --config /etc/etcd-backup-keeper/config.toml
sudo etcd-backup-keeper verify --config /etc/etcd-backup-keeper/config.toml
```

`--verbose`는 stderr에 단계·수신 바이트를 표시하며 ETA나 복원 성공을 뜻하지 않습니다.
`status`는 오프라인 목록·메타데이터 조회로 **전체 해시 검사를 하지 않습니다**.
`verify`는 기본적으로 **모든 완료 백업**을 검사하고 완료 백업이 없으면 실패합니다.
두 명령 모두 설정 형식을 검증하지만 etcd에 접속하거나 인증 파일을 읽지 않습니다.
정확한 완료 이름 하나의 검사와 JSON·종료 코드는 [운영 기준](operations.md#3-실행조회검증)에 있습니다.

실패하면 다음 명령을 그대로 실행하지 말고 종료 코드와 [문제 해결](troubleshooting.md)을
확인하세요. 새 백업이 있어도 sync·보관 정리 실패 등으로 비정상 종료할 수 있습니다.
`.lock`, `inprogress-*`, `deleting-*`를 임의로 지우지 마세요.

## 5. 복원 훈련·수용 확인 후 timer 활성화

로컬 verify 성공은 복원 성공 보증이 아닙니다. [격리 복원 훈련](restore-drill.md)으로
별도 인스턴스 복원과 데이터 읽기를 확인하고, 실제 호스트의 systemd sandbox·저장 볼륨·
용량·감시·스케줄을 수용한 뒤에만 활성화합니다. 오프라인 패키지 검사는 실제
service/timer 실행 성공을 증명하지 않습니다.

**`Persistent=true`이므로 timer를 시작하면 누락된 실행이 곧 시작될 수 있습니다.**
기본은 daily, 최대 15분 무작위 지연입니다. 지금 백업이 시작되어도 되는 시점에 실행하세요.

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now etcd-backup-keeper.timer
systemctl list-timers etcd-backup-keeper.timer
sudo journalctl -u etcd-backup-keeper.service
```

이후 정기 확인, hardening, timeout·저장소 변경, **업그레이드 전 timer 중지·실행 중
작업 완료 대기·설정과 백업 보존**은 [운영 안내](operations.md#5-systemd-스케줄러와-패키지-수명주기)를 따릅니다.
