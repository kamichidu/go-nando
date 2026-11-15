# ローカル quickstart

以下は daemon と CLI を一つの OS account で使う例です。

1. repository を作成します。

   ```sh
   nando --repository "$HOME/nando-data" init
   ```

2. daemon を起動し、別の terminal を開きます。

   ```sh
   nando --repository "$HOME/nando-data" daemon
   ```

3. ファイルを登録して一覧・取得します。

   ```sh
   nando --repository "$HOME/nando-data" put ./photo.jpg /photo.jpg
   nando --repository "$HOME/nando-data" ls /
   nando --repository "$HOME/nando-data" get /photo.jpg ./photo-copy.jpg
   ```

daemon は repository を使う間起動しておきます。毎回 `--repository` を指定する代わりに、user TOML の `[repository].path` を使えます。
