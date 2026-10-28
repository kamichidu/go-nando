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

Design IR、通常・race uncached全体suite、lint、Windows build/test cross-compile、diff checkが通過。Phase 4実装にSQLite競合の最小修正と回帰テストを加えた。Windows runtime上での置換テストは未実行。ローカルの全変更をレビュー後にcommit/pushする。
