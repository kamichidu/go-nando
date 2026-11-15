# Local quickstart

This example uses the daemon and CLI under the same operating system account.

1. Create a repository.

   ```sh
   nando --repository "$HOME/nando-data" init
   ```

2. Start the daemon and open another terminal.

   ```sh
   nando --repository "$HOME/nando-data" daemon
   ```

3. Import, list, and retrieve a file.

   ```sh
   nando --repository "$HOME/nando-data" put ./photo.jpg /photo.jpg
   nando --repository "$HOME/nando-data" ls /
   nando --repository "$HOME/nando-data" get /photo.jpg ./photo-copy.jpg
   ```

Keep the daemon running while using the repository. To avoid passing `--repository` each time, set `[repository].path` in the user TOML file.
