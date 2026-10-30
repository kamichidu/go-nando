# Phase 4 実装・検証レポート

## 基準と変更範囲

- 開始時の `main` / `origin/main`: `6d6a26ba124f16e17755ad3fb3d8c4c7ab5538ce`
- Design IRを先に更新し、validator `VALID` を確認してから実装した。
- Phase 3のRepository exclusiveはGet/Put/GC/Doctorに維持。Operation Journal、resume、generic Repair、tombstone/compaction、crash staging cleanupは実装していない。

変更ファイル:

- `docs/design.ir.yaml`
- `internal/repository/get.go`
- `internal/repository/replace_unix.go`
- `internal/repository/replace_windows.go`
- `internal/repository/phase4_test.go`
- `report.md`

## 実装内容

### Get / Export

`internal/repository/get.go` は最終destinationをtruncateせず、destinationと同じdirectoryに一時fileを作成します。Objectを全てcopyし、metadata由来のmode/mtimeを設定してSync・Closeした後にのみ公開します。Object readerのCloseエラーも公開前に検出します。失敗時は一時fileを除去し、既存destinationを維持します。

destinationはLstatで確認し、symlink、directory、regular file以外は拒否します。symlinkのリンク先は開かず、symlink自体も書き換えません。destinationが存在しなければ新規作成します。GetのRepository排他期間は変更していません。

- Unix (`replace_unix.go`): 同directory `os.Rename`。Unix renameの置換は同一filesystem内のatomic renameを使う。
- Windows (`replace_windows.go`): 既存fileは同volumeの`ReplaceFileW`をbackup名付きで呼び、失敗時にbackupから復元を試みる。不存在destinationは`MoveFileExW`を置換なしで呼ぶため、検査後に別fileが作られていれば上書きせず失敗する。公開後にbackupを除去できない場合は `ErrDestinationPublished` を返し、出力が既に反映済みと識別できる。

Goの[`os.Rename`仕様](https://pkg.go.dev/os#Rename)は非Unixでatomic性を保証しないため、Windowsは標準の`os.Rename`に依存させず、[`ReplaceFileW`](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-replacefilew)と[`MoveFileExW`](https://learn.microsoft.com/en-us/windows/win32/api/winbase/nf-winbase-movefileexw)を使う。Windows側は同directory/同volumeのlocal filesystemを前提とする。

### Doctor / GC / commit境界

- IRのDoctor契約を更新し、validな未参照ObjectはDoctorの異常扱いにせずGC責務とした。Doctorの実装はread-only検査のまま。
- PutでObject Commit前失敗、Objectが公開された後にCommit error、Metadata永続化前失敗、Metadata Create/Updateの永続化後errorを注入するテストを追加した。既存のreplacement Metadata Update失敗テストも維持される。
- Metadata commit前に失敗したObjectは未参照となり、後続GCで削除可能。Metadata commit成功後に呼出しがerrorを返してもEntryは既に公開済みで、GCが参照Objectを維持することをテストした。これはCoordinatorのrelease error同様、errorだけで「未反映」と判断できない境界を確認する。
- GCが複数orphanを削除する途中で失敗するケースと、再実行で残りだけを削除し、参照Objectを維持するケースを追加。
- Doctor前後のMetadata Entries/Object一覧を比較し、非破壊であることとvalid orphanを報告しないことを確認。

### staging cleanup

通常error return時のbackend-owned staging cleanupは既存Local Object Store実装を維持。process crash後のstaging残骸は削除しない。別プロセスが現在書き込み中のstagingを誤削除しない所有/排他方式が決まっていないため、`docs/design.ir.yaml` に `OPEN-PHASE4-STAGING-CLEANUP` を追加した。startup掃除は行わない。

## Design IRの更新

- Get requirementを同directory temporary file、属性設定後の置換、不存在作成、symlink/directory拒否、置換前失敗での旧destination維持とtemporary cleanupに具体化。
- Windowsで置換後backup cleanupだけが失敗した場合は、destinationが既に公開済みとエラーで識別する契約を追加。
- DoctorをRepository health検査に限定し、valid orphanをGC責務と明記。
- Phase 4 goal/exit criteriaをfailure boundaries、atomic Get、Doctor非破壊、GC再実行可能性に更新。tombstone/compactionをPhase 4から外し、既存`OPEN-DELETE-RETENTION`は未決定のまま保持。
- crash後staging cleanupはactive writer保護の安全方式が未決定としてOpen Itemに保持。

## テストと実測

| Check | 結果 | wall-clock |
|---|---:|---:|
| `go test -race -count=20 ./internal/metadata/sqlite -run '...same destination...'` | pass | 3.21秒 |
| SQLite lock-wait Context + non-conflict SQL regression tests, race, count=5 | pass | 2.27秒 |
| `go test -count=1 ./...` | pass | 2.53秒 |
| `go test -race -count=1 ./...` | pass | 5.50秒 |
| `go test -race -p 1 -count=1 ./...` | pass | 14.25秒 |
| `golangci-lint run ./...` | 0 issues | 2.46秒 |
| Design IR validator (`DESIGN_IR_REPO=/home/kusogaki/workspaces/design-ir`) | `VALID` | 0.73秒 |
| `GOOS=windows GOARCH=amd64 go build ./...` | pass | 1.14秒 |
| Windows SQLite test cross-compile (`go test -c`) | pass | 0.80秒 |
| Windows Repository test cross-compile (`go test -c`) | pass | 0.65秒 |
| `git diff --check` | pass | 0.00秒未満（コマンド列内） |

### SQLite concurrent MoveのBUSY修正

uncached raceで再現した原因は、SQLiteの既定DEFERRED transactionが各Moveの読取後にwriter lockへ昇格する競合だった。競合した読み取りtransactionは有効なsnapshotから同時に昇格できず、5件すべてが`SQLITE_BUSY`になる場合がある。

SQLite DSNを`_txlock=immediate`にし、各書込transactionが最初のSELECTより前にwriter予約lockを取得するようにした。SQLiteのbusy handlerはContext deadlineより先に待ち続けることをdriver動作で確認したため、busy timeoutには頼らず、BEGINから返る`SQLITE_BUSY`だけを5ms Context-aware waitで再試行する。cancel/deadlineは`ctx.Err()`を返し、BUSY以外のbegin errorは変更せず返す。SQLite driverのerror code primary byteでBUSYのみを判定する。foreign_keysはDSN PRAGMAで各接続に適用する。

MoveのUPDATEでnamespace unique制約以外のSQLite errorを`ErrEntryConflict`に包んでいたため、その誤分類も外した。回帰テストは、writer lock待ちのdeadline、キャンセル後の次操作成功、非制約SQL障害のerror分類を確認する。既存の同一宛先Moveテストは5件中ちょうど1件成功を引き続き要求する。

race suiteは並列実行・`-p 1`ともに完走。race付き同時Moveテストはuncachedで20回連続成功した。Windows上のnative runtimeは使えないため、Windows buildおよびSQLite/Repository test binaryのcross-compileまで実施した。

現環境はLinuxで、Windows test binaryはcross-compileのみ。Windows上で並行reader testと失敗経路を実行するnative runtime検証は未実施。Go標準docsは非Unixの`os.Rename`をatomicと保証していないため、Windows runtimeでの置換stress/error-semantics testをCI等で実行することが残る検証上の制約。

## 保証と未対応

- 保証対象: 通常のObject read/copy/attribute/Sync/Close失敗では、既存通常file destinationを置換せずtemporary fileを掃除する。symlink/directory/non-regular destinationを拒否する。置換成功前に公開しない。GetはRepository排他を維持する。
- Windows native test runtime未実行のため、実環境での置換stress確認は未完了。`ReplaceFileW`自体が失敗しbackupからの復元もOS errorで失敗した場合、元fileはbackup pathに残り得る。復元errorは元のAPI errorと合わせて返す。
- `ErrDestinationPublished` はWindowsの置換後backup cleanup errorで出力反映済みを示す。backupが残存する場合がある。
- process crash後のLocal staging残骸cleanupは未実装・Open。active staging保護方法が決まるまで削除しない。
- generic Repair、tombstone、compaction、operation journal/resumeは対象外。
- 任意の外部processがdestination pathを同時変更する競合、およびnetwork filesystemでの原子性は保証しない。

## 最終状態

Design IR、通常・race uncached全体suite、lint、Windows build/test cross-compile、diff checkが通過。Phase 4実装にSQLite競合の最小修正と回帰テストを加えた。Windows runtime上での置換テストは未実行。このPhase 4一式はcommit `95c7d97b40a2d85be3aeb6cefeb9781eddfdc320` としてorigin/mainへpush済み。

---

## Put Stage / Commit と操作Lease

### 設計

`docs/design.ir.yaml`を先に更新し、Design IR v1alpha1 validator `VALID` を確認してから実装した。PutのStage/Commit分割、operation_id/expires_atのみを持つLease、renewal timing、expiry fencing、cleanup retryを定義した。Lease heartbeatをPhase 3の`coordination.db`に置くと、全Read期間保持する`BEGIN IMMEDIATE`で長時間Cat/Getがrenewalを止める。この競合を避けるためLease記録を別の`put-leases.db`に分離し、CommitのLease検証と期限失効は既存Coordinator排他で直列化する。

期限はSQLite database clockのUnix millisecondsで比較する。Lease TTLは30秒、heartbeatは10秒ごと。StageはLeaseを取得する前にoperation ID固有のstaging pathを`O_EXCL`で一度だけ作成してfile descriptorを保持し、Lease取得後も同じdescriptorからcopy/hashする。pathがcleanupや手動削除で消えた後は再open/createしない。削除時に既にwrite中なら開いたinodeへの書き込みは完了し得るが、pathがないためPublishできずPutは失敗する。期限切れLeaseはRepository Coordinator排他下で先に`expires_at=0`へ無効化し、その後stagingを削除する。削除失敗時はinvalidated Lease行を保持し、次回Put/GCで再試行する。Stage fileだけ残りLeaseが存在しないクラッシュ残骸も列挙してcleanup対象にする。

Local Object Storeはoperation ID別のstaging fileを使う。OS依存file lockやbackend-private Stage lockは設けず、stagingは削除されてもPutが失敗するだけの一時領域として扱う。Stageのcopy/hashはRepository全体のexclusive lock外、親Directory作成・Object publish・Metadata commitは内側。GC/Doctor/Get/Catの既存排他境界を維持した。GCSは既存stubのままで、新しいStage APIも未実装。

### 主な変更

- `internal/repository/put.go`: Stage/Commit分割、期限付きLease renewal、Commit開始時のlease検証、後処理時の`ErrPutCommitted`分類。
- `internal/coordinator/sqlite/coordinator.go`: `put-leases.db`、条件付きrenewal、排他下のexpiry invalidation、cleanup完了後の行削除。
- `internal/objectstore/store.go`, `internal/objectstore/local/stage.go`: backend-owned Stage session、immutable staged handle、publish、operation別cleanup。
- `internal/objectstore/local/stage.go`: one-shot staging file、既存descriptorへのStage、path不在時のPublish拒否、staging列挙・cleanup。
- `internal/repository/repository.go`: GC前にexpired staging cleanup。
- `docs/design.ir.yaml`: normative Stage/Lease/fencing/cleanup契約とPhase 3 coordination記述。

### 回帰・競合テスト

- 複数Coordinator instance/process間のLease更新とexpiry race。失効後renewalが成功しないこと。
- 長時間Repository exclusive read中にLease renewalが独立DBで進むこと。
- active Stage中にcleanupがunlinkしても、完了したstagingがPublishできず、Stage pathが再生成されないこと。
- expiry後のstale StageがCommit callbackへ入らず、cleanup後に同じIDで遅れて始めたStageもrenewできないこと。
- cleanup失敗後にinvalidated Leaseを保持し、次のGCで再試行できること。
- Put Stage中はGCが進み、Put Commitに入ると従来どおりCoordinator排他が効くこと。
- 別processのStage sessionがpath削除後に再生成しないこと。

### 最終検証と実測

| Check | 結果 | wall-clock |
|---|---:|---:|
| `go test -count=1 ./...` | pass | 2.11秒 |
| `go test -race -count=1 ./...` | pass | 6.95秒 |
| `go test -race -p 1 -count=1 ./...` | pass | 17.38秒 |
| Lease race/process tests, race, count=5 | pass | 7.98秒 |
| Local Stage isolation and cleanup race tests, race, count=1 | pass | 3.45秒 wall-clock (orphan staging test追加前) |
| `golangci-lint run ./...` | 0 issues | 2.08秒 |
| Design IR validator | `VALID` | 0.57秒 |
| Windows amd64 build | pass | 0.74秒 |
| Windows coordinator/local-objectstore/repository test cross-compile | pass | 0.61/0.52/0.75秒 |
| macOS arm64 build and local-objectstore test cross-compile | pass | 0.79/0.51秒 |
| `git diff --check` | pass | 上記cross-build列の末尾で実行 |

### 残課題・境界

- Windows/macOSのnative runtime実行環境はないため、open staging file unlink/cleanup挙動はLinuxで実測し、他OSはcross-compileまで。
- crash cleanupは次回PutまたはGCが呼ばれた時に遅延実行される。常駐cleanup workerやstartup掃除は追加していない。
- GCS backendは未実装stubのため、Stage API/Lease cleanupを実装していない。
- `put-leases.db`の現在の初期schemaはidempotentな単一table作成で、schema migration ledgerはない。将来Lease schemaを変更する場合は互換運用を別途設計する。
- Leaseはstaging再利用/Put全体exactly-once、Object availability、GC保護を保証しない。Object Commit後・Metadata commit前のfailureは既存GCへ委ねる。

今回のStage/Commit一式はローカル作業ツリーに残し、commit/pushしていない。

### Stage cleanup lockless修正

OS依存のstaging lockを削除し、SQLite LeaseをCommit権限の唯一の根拠とした。Local `BeginStage` はoperation IDごとのpathを`O_EXCL`で一度作り、sessionはそのfile descriptorを保持する。Stageはdescriptorを再openせずにcopy/hashし、StageSessionは一度だけ使用できる。cleanupまたは手動削除後はStage/Publishがpath不在を検出して失敗する。cleanupが既に実行中のwriteと重なり、開いたinodeへのwriteが完了しても、unlink済みpathをPublishできない。

RepositoryはStage path作成後にLeaseを取得し、Stage直前にconditional renewalする。Commit内のLease検証/Object publish/Metadata commitと、失効Leaseの無効化は同じRepository Coordinatorで直列化する。失効Leaseを先に無効化し、その後stagingを削除する。削除失敗時はLease行を削除せず、staging pathを列挙して次回Put/GCで再試行する。staging pathだけが残った場合も、active Lease一覧にないoperation IDとしてcleanupする。cleanupが新しいLease取得との競合中にpathを削除した場合は、そのPutが失敗することを許容する。

削除したもの:

- `internal/objectstore/local/stage_lock_unix.go`
- `internal/objectstore/local/stage_lock_windows.go`
- `ErrStagingInUse`とOS lock helper/API

追加・更新したテストは、進行中Stage中のunlink、unlink後のPublish拒否、手動削除後の再生成防止、複数プロセスでのactive Stage unlink、失効Lease fencing、cleanup失敗後の再試行を確認する。失敗したStageのpartial fileはStage自身が消さず、RepositoryのLease無効化後cleanupに委ねる。

今回の修正ゲート実測:

| Check | 結果 | wall-clock |
|---|---:|---:|
| `go test -count=1 ./...` | pass | 2.21秒 |
| `go test -race -count=1 ./...` | pass | 6.68秒 |
| `golangci-lint run ./...` | 0 issues | 1.37秒 |
| Design IR validator | `VALID` | 0.56秒 |
| Windows amd64 `go build ./...` | pass | 0.49秒 |
| Windows local Object Store test cross-compile | pass | 0.55秒 |
| `git diff --check` | pass | 0.00秒 |

全体テストはLinux上で実行した。Windows cross-buildはpassしたが、native Windowsのopen-file削除/再試行 semanticsは未実測。変更は未commit。
