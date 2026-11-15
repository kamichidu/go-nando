# FUSE

`nando-fuse` は別 process の filesystem adapter です。対応する platform に FUSE runtime が必要です。Debian package では `nando fuse` subcommand が adapter を起動します。

```sh
nando fuse --repository "$HOME/nando-data" --fuse-mountpoint "$HOME/nando-mount"
```

FUSE mount options は `[fuse].mount_options` または repeatable `--fuse-mount-option` で指定できます。対応する値は `allow_other` と `default_permissions` です。いずれも既定で無効で、`allow_other` には host の `/etc/fuse.conf` 設定が必要です。

正常終了時は mount を解除してから daemon を停止します。FUSE の close 完了だけでは同期的な commit 境界にならない場合があるため、確実な可視化が必要な書き込みでは fsync を使います。
