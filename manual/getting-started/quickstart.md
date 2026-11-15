# Quick start

This local example uses the current account and the XDG-style repository default. With the defaults, the repository is `${XDG_DATA_HOME:-~/.local/share}/nando`; an explicit `--repository` is only needed when you want a different location.

1. Initialize the repository.

   ```sh
   nando init
   ```

2. Start the daemon in a terminal and leave it running. Open another terminal for the client commands.

   ```sh
   nando daemon
   ```

3. Import a file, list the repository, and retrieve a copy.

   ```sh
   nando put ./photo.jpg /photo.jpg
   nando ls /
   nando get /photo.jpg ./photo-copy.jpg
   ```

The commands share the same default repository and repository-derived local daemon socket. If you set `[repository].path` or use `--repository`, use that same selection for `init`, the daemon, and client commands. The daemon must remain running for normal repository operations. For ordinary Linux service operation, use the [systemd setup](../setup/systemd); manual foreground startup is useful for this quick check and non-systemd environments.
