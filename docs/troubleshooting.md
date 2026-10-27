# 문제 해결

첫 설치·kubeadm 설정은 [빠른 시작](quickstart.md), 정확한 출력·종료 코드와 저장소
계약은 [운영 안내](operations.md)가 기준입니다. 오류를 숨기거나 안전 검사를 우회하지
말고, 대상·실행 UID·설정·현재 백업 상태를 먼저 확인하세요.

## 조사 시작

- 실패한 명령, 시각, 바이너리 `version`, 종료 코드와 `error [CODE]`를 기록합니다.
  자동화는 `--json`의 `ok`·`error.code`와 프로세스 종료 코드를 함께 확인합니다.
  JSON stdout은 단일 object이며 `backup --verbose` 진단은 stderr입니다.
- 오류·timeout·취소 후에는 작업이 완전히 종료했는지 확인합니다. service라면
  `systemctl status etcd-backup-keeper.service`와
  `sudo journalctl -u etcd-backup-keeper.service`를 확인합니다. 수동 작업·외부
  스케줄러도 별개로 확인하며 timer만 멈췄다고 현재 service가 끝난 것은 아닙니다.
- 같은 UID·설정으로 `status`를 실행해 완료·미완료·삭제 대기를 확인합니다.
  잠금 중이면 끝날 때까지 기다립니다. `status` 성공은 전체 해시 검사·내구성·복원 보증이
  아닙니다. `.lock` 삭제·교체, 광범위한 재귀 삭제, 완료 이름으로 수동 rename을 하지 마세요.
- 비밀번호·개인키·스냅샷·민감한 데이터나 전체 설정을 공개 로그에 붙이지 마세요.
  제품 오류·hint는 정적 문구로 일반화되며 원본 네트워크 오류를 공개하지 않습니다.

## kubectl과 localhost:8080

`The connection to the server localhost:8080 was refused` 또는
`http://localhost:8080` 접속 오류는 이 안내의 **kubectl에 사용할 kubeconfig가 없거나
의도한 설정이 선택되지 않았을 가능성**부터 확인합니다. `8080`을 etcd endpoint로
설정하거나 API server의 보안을 낮추지 마세요.

kubeadm control-plane의 승인된 관리자만 다음처럼 파일 존재·접근 권한과 명시적 선택을
확인합니다. `sudo` 실행 시 일반 사용자의 `KUBECONFIG` 환경이 유지된다고 가정하지 않습니다.

```sh
sudo test -r /etc/kubernetes/admin.conf
sudo kubectl --kubeconfig=/etc/kubernetes/admin.conf -n kube-system \
  get pods -l component=etcd -o wide
```

여전히 실패하면 파일이 실제 대상 클러스터의 kubeconfig인지, API server 주소·CA·관리자
인증·네트워크를 승인된 절차로 확인하세요. kubeconfig 내용을 공개하거나 파일을 일반
사용자에게 광범위하게 읽히도록 바꾸지 마세요. 외부 백업 노드나 비-kubeadm 환경에는
이 경로가 없을 수 있습니다. 올바른 kubeconfig를 별도 제공받거나 선택적 ID 조회를
생략합니다. 제품 CLI는 kubeconfig나 Kubernetes API를 사용하지 않습니다.

## 설정·TLS·원격 오류

| 오류 코드 | 확인과 안전한 조치 |
|---|---|
| `usage` | `COMMAND --help`로 해당 명령의 옵션 확인. `--verbose`는 backup, `--backup`은 verify, `--connect`는 check 전용 |
| `config_read` | 의도한 `--config` 경로, 일반 파일 여부와 실행 UID의 읽기 권한 확인. 기존 설정을 example로 덮어쓰지 않음 |
| `config_invalid` | [설정 계약](operations.md#설정-계약)의 정확한 소문자 키·TOML·절대 경로·timeout·쌍 옵션 확인. 설명용 cluster ID를 활성화하지 않음 |
| `credentials_invalid` | 개인키·password 파일의 실행 UID 소유, private 일반 파일, group/other 권한 없음, 마지막 symlink 없음 확인. 비밀번호는 private `password_file` 한 줄로 배치 |
| `tls_invalid` | 공개 CA 인증서, client 인증서/키 쌍과 파일 형식 확인. 접속 시에는 유효기간·서버 SAN·신뢰 CA도 확인. CA/server/peer 개인키를 client용으로 쓰거나 TLS 검증을 끄지 않음 |
| `remote_unavailable` / `remote_invalid` | endpoint 도달성·서버 상태·방화벽·DNS·TLS 신뢰/서버 SAN·client 인증과 snapshot 권한 확인. 로컬 check 성공도 원격 신뢰·권한 성공은 아님 |

연결 단계 TLS·인증 실패는 로컬 파일 로딩의 `tls_invalid`가 아니라
`remote_unavailable`로 보일 수도 있습니다. 주소를 바꾸기 전에 실제 서버와 인증서 구성을
대조하세요. stacked etcd와 외부 etcd/원격 호스트의 endpoint·client 인증 쌍은 다릅니다.
`check --connect`는 후보 **전부**의 Status를 점검하므로 하나의 실패를 다른 후보 성공으로
숨기지 않습니다. Status 성공도 snapshot 권한은 확인하지 않습니다.

### server_unsupported / storage_unsupported

현재 지원은 안정판 서버 **etcd 3.6.x / 3.7.x**, 저장 형식 **3.6.0 / 3.7.0**입니다.
서버 patch 버전과 저장 형식은 독립적으로 검사합니다. 롤링 업그레이드 중 서로 다른
minor여도 각각 이 범위여야 하며 prerelease·다른 minor/major·알 수 없는 버전은 거부합니다.

- `server_unsupported`: 실행한 **백업 도구의 version**과 실제 서버 버전을 함께 확인합니다.
  이전 `v20261027-1`은 3.7 전용이었습니다. 3.6 서버를 만나는 경우 현재 지원 범위를
  확인하고 [정상 업그레이드 절차](operations.md#5-systemd-스케줄러와-패키지-수명주기)를 따릅니다.
  이 오류를 없애려고 운영 etcd를 즉석에서 업그레이드하지 마세요.
- `storage_unsupported`: snapshot의 저장 형식과 실행 도구의 지원 범위를 확인하고 원본·
  기존 백업·staging을 보존합니다. 서버 버전만으로 저장 형식을 추정하지 마세요.
- 버전 우회 옵션은 없습니다. metadata/DB를 편집하거나 알 수 없는 형식을 지원된
  것으로 가장하지 마세요. 지원 밖 환경은 별도 호환성 검토가 필요합니다.

### cluster_mismatch

후보끼리, 설정의 `expected_cluster_id`, 기존 완료 백업 또는 snapshot 신원 사이의
불일치입니다. 의도하지 않은 클러스터의 스냅샷을 같은 저장소에 섞지 않도록 중단합니다.

1. 후보 URL과 TLS·클러스터 매핑을 확인하고 모든 endpoint가 같은 클러스터인지 조사합니다.
2. 설정 ID가 **정확한 16진수**인지 확인합니다. kubeadm의 선택적 조회는
   [호스트 Python 변환 예시](quickstart.md#선택-사항-실제-cluster-id를-확인해-pin-설정)를 따릅니다.
   JSON의 큰 10진 ID를 부동소수점으로 변환하면 다른 ID가 될 수 있습니다.
3. 기존 완료 백업과 staging을 보존합니다. pin을 지우거나 metadata를 고쳐 통과시키지
   마세요. 의도된 클러스터 교체라면 별도 승인된 private 저장소와 이관 정책을 준비합니다.

`check --connect`는 기존 완료 백업의 pin을 읽지 않으므로 성공해도 실제 backup에서
불일치할 수 있습니다. ID 확인도 snapshot 스트림의 암호학적 출처 증명은 아닙니다.

## 저장소·공간·검증 오류

| 오류 코드 / 상태 | 확인과 안전한 조치 |
|---|---|
| `storage_locked` | backup의 배타 잠금 또는 status/verify의 공유 잠금과 충돌. 현재 작업이 끝날 때까지 기다리고 스케줄 중복 조정. `.lock`을 삭제·교체하지 않음 |
| `storage_unsafe` | [저장소 준비](operations.md#1-실행-환경과-저장소-준비)에 따라 실행 UID 소유·정확한 0700/0600·상위 경로·symlink/특수 파일/hard link 확인. 일괄 chmod/chown이나 강제 삭제 전에 증거 보존 |
| `space_insufficient` | 실제 백업 볼륨의 가용 바이트·inode, `min_free_bytes`, 미완료·삭제 대기 사용량 확인. 기존 완료 백업을 임의로 지워 우회하지 않음 |
| `snapshot_invalid` / `metadata_invalid` | 해당 파일·로그 보존 후 손상·변조·불완전 쓰기 조사. 해시 manifest나 metadata를 재생성해 통과시키지 않음. 신뢰할 수 있는 다른 완료 백업의 verify와 격리 복원 검토 |
| `no_backups` | status로 의도한 저장 경로 확인. verify는 완료 백업이 없으면 실패하며 staging은 대체물이 아님 |
| `backup_not_found` | `--backup`에 status의 정확한 완료 디렉터리 이름 하나를 사용. 경로나 미완료 이름을 지정하지 않음 |
| `backup_incomplete`, `inprogress-*` | 완료 백업이 아님. 자동 삭제·승격하지 않음. 작업 종료·로그·공간을 확인하고 [미완료 수동 점검](operations.md#미완료-항목의-수동-점검) 절차로 조사 |
| `deleting-*` | 삭제 대기 항목을 보존·조사. status/verify는 정리를 재개하지 않으며 다음 성공적인 새 백업 확정 후 안전 검사를 거쳐 재시도 |

`status`의 `Completed snapshot bytes`는 완료 snapshot 파일 크기의 합이며 전체 볼륨
사용량·여유 공간이 아닙니다. 최근 완료 시각은 최근 시도나 마지막 verify 기록도 아닙니다.
완료 백업 무결성은 별도 `verify`, 복원 가능성은 [격리 복원 훈련](restore-drill.md)으로 확인합니다.

## 확정 후 실패: durability_uncertain과 retention_incomplete

**새 백업 이름이 보여도 성공으로 간주하거나 무조건 재실행하지 마세요.** JSON은
`ok: false`여도 `data`에 확정된 백업 정보가 있을 수 있습니다.

- `durability_uncertain`: rename 후 **백업 루트 sync가 실패**해 이름은 있어도 내구성이
  불확실합니다. 보관 정리는 실행하지 않았습니다. 볼륨·장치·fsync 실패부터 조사하고
  기존 백업을 보존합니다. status나 verify 성공도 정전 내구성을 증명하지 않습니다.
- `retention_incomplete`: 새 백업 확정·루트 sync 이후 **보관 정리가 미완료**입니다.
  status로 완료·삭제 대기를 구분하고 필요 시 정확한 완료 이름으로 verify합니다.
  권한·공간·저장장치·예상 밖 파일 등 정리 실패 원인을 확인한 뒤 다음 작업을 결정합니다.
- 확정 후 `timeout`/`canceled`는 그 코드만으로 어느 단계까지 끝났는지 추정하지 않습니다.
  작업 종료를 기다린 뒤 status·로그·보존된 staging을 확인합니다. SIGKILL·호스트 장애는
  정상 취소나 마지막 sync를 보장하지 않습니다.

확정·정리 순서와 개별 파일 수동 점검은 [운영 계약](operations.md#4-확정보관-정리와-실패-대응)을
따릅니다. 오류를 없애기 위한 `.lock` 제거·재귀 삭제·TLS/버전 검사 우회는 해결책이 아닙니다.

## timer·업그레이드 문제

설치·업그레이드는 자동 enable/start·백업을 하지 않습니다. 기존 활성 timer도 자동으로
멈추지 않습니다. [수명주기 절차](operations.md#5-systemd-스케줄러와-패키지-수명주기)에 따라
**먼저 timer 중지, 현재 작업 완료 대기 또는 명시적 취소, 설정·백업 보존**을 수행하세요.
`Persistent=true`는 활성화 시 누락 실행을 곧 시작할 수 있습니다.

수동 root 실행은 성공하는데 service가 실패하면 journal과 unit의 `ProtectHome`,
`ReadWritePaths`, `TimeoutStartSec` 및 실제 자료 경로를 대조합니다. sandbox를 일괄
해제하지 마세요. offline 패키지·정적 unit 검사는 실제 service 실행의 증거가 아닙니다.
