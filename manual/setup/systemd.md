# systemd system service

Debian package は `nando@.service` の system template と専用の非 login `nando` account をインストールします。system service は専用 account で動く supported deployment です。socket は owner-only のため、CLI、FUSE、WebDAV も同じ `nando` account と同じ endpoint を使います。通常の login account と socket を共有する機能はありません。user manager で運用する場合は package 同梱の `nando@.service` user template を使えます。

## 1つの instance を設定する

次の例では instance 名 `archive` と repository `/var/lib/nando/archive` を使います。

1. package をインストールします。

   ```sh
   sudo dpkg -i ./go-nando_*_linux_amd64.deb
   ```

   package の maintainer script は systemd unit を reload しますが、service の enable/start や repository の初期化はしません。

2. 設定 directory を作成し、同梱 sample をコピーします。

   ```sh
   sudo install -d -o root -g nando -m 0750 /etc/nando
   sudo install -o root -g nando -m 0640 \
     /usr/share/doc/go-nando/config.system.example.toml \
     /etc/nando/archive.toml
   ```

3. `/etc/nando/archive.toml` で repository path を `/var/lib/nando/archive`、daemon listen を `unix:///run/nando-archive/control.sock` にします。Remote を使うときは scheduler 値をすべて設定します。GCS ADC や AWS credentials は package 外で `nando` account に安全に provision します。
4. service account として repository を初期化します。

   ```sh
   sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml init
   ```

5. unit を有効化し起動します。

   ```sh
   sudo systemctl daemon-reload
   sudo systemctl enable --now nando@archive.service
   sudo systemctl status nando@archive.service
   ```

6. 同じ identity で daemon に接続できることを確認します。

   ```sh
   sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml doctor
   ```

必要な repository operation も `sudo -u nando /usr/bin/nando --config /etc/nando/archive.toml ...` で実行します。別の OS account から接続するには対応する認可設計が必要で、socket mode を緩める方法はサポートしません。

## FUSE / WebDAV

adapter は別 process です。daemon を先に起動し、adapter にも同じ config を渡して同じ OS account で起動します。FUSE mountpoint の所有権と OS の FUSE policy は host 側で設定してください。system service は mount adapter を自動起動しません。

## 更新と削除

package upgrade は稼働 daemon を自動 restart しません。release note を確認し、保守時間に instance を再起動します。削除時は enabled service を手動で disable し、package を削除します。repository、設定、credential は保持されます。
