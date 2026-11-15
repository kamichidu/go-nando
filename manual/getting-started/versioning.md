# バージョンと更新ポリシー

`v1.0.0` を互換性保証の開始点とします。`v1.0.0` より前の repository、設定、CLI、schema、Recovery state との互換性は保証しません。

| SemVer 部分 | 方針 |
| --- | --- |
| Patch | 後方互換な不具合修正 |
| Minor | 後方互換な機能追加 |
| Major | 破壊的変更 |

この互換ポリシーはバイナリの自動更新を意味しません。新しいリリースのインストールと稼働 daemon の再起動は、利用者または管理者が計画して行います。

Repository format と metadata schema は別々に version 管理します。将来の schema 変更を行う Minor release は、その直前 Minor が作った schema から一段だけ自動移行する設計方針です。複数 Minor を飛び越す移行とダウングレード移行は提供しません。現行 v1 baseline は schema version 1 で、一般化された将来の Minor 間移行はまだ実装されていません。現在の `nando admin migrate` は v1 schema の検証のみを行います。

Repository format を異なる version 間で自動移行する処理も、現行実装にはありません。unsupported state は自動書き換えせず拒否します。
