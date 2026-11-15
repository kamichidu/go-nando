# 基本概念

| 概念 | 意味 |
| --- | --- |
| Repository | 設定、metadata database、local objects、staging を含む一つの保管単位 |
| Entry | repository 内のファイルまたはディレクトリ名と metadata |
| Object | content ID で識別する不変のファイル本体 |
| daemon | repository を開き、CLI、FUSE、WebDAV からの操作を処理するプロセス |
| Local mode | Local object storage を耐久性のある保存先として使うモード |
| Remote mode | Local に commit してから、一つの選択済み remote backend へ非同期配送するモード |

remote 配送を有効にしても、Entries の可視性は metadata が決めます。Local と Remote は一 repository で選ぶモードであり、複数 provider への複製ではありません。
