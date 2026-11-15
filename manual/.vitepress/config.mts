import { defineConfig } from 'vitepress'

export default defineConfig({
  lang: 'ja-JP',
  title: 'nando マニュアル',
  description: 'nando のインストール、利用、運用ガイド',
  base: '/go-nando/',
  cleanUrls: true,
  themeConfig: {
    nav: [
      { text: 'はじめに', link: '/getting-started/install' },
      { text: 'セットアップ', link: '/setup/repository' },
      { text: '利用', link: '/use/cli' },
      { text: '運用', link: '/operations/daemon' },
      { text: 'リファレンス', link: '/reference/cli' },
    ],
    sidebar: [
      { text: '概要', items: [{ text: 'nando とは', link: '/' }] },
      { text: 'はじめに', items: [
        { text: 'インストール', link: '/getting-started/install' },
        { text: 'ローカル quickstart', link: '/getting-started/quickstart' },
        { text: '基本概念', link: '/getting-started/concepts' },
        { text: 'バージョンと更新', link: '/getting-started/versioning' },
      ] },
      { text: 'セットアップ', items: [
        { text: 'Repository の初期化', link: '/setup/repository' },
        { text: '設定ファイル', link: '/setup/configuration' },
        { text: 'Google Cloud Storage', link: '/setup/gcs' },
        { text: 'S3 / Wasabi', link: '/setup/s3' },
        { text: 'systemd system service', link: '/setup/systemd' },
      ] },
      { text: '利用', items: [
        { text: 'CLI', link: '/use/cli' },
        { text: 'FUSE', link: '/use/fuse' },
        { text: 'WebDAV', link: '/use/webdav' },
        { text: 'Remote 配送', link: '/use/remote-delivery' },
      ] },
      { text: '運用', items: [
        { text: 'daemon', link: '/operations/daemon' },
        { text: 'Garbage collection', link: '/operations/gc' },
        { text: 'Backup と restore', link: '/operations/backup-restore' },
        { text: 'Recovery', link: '/operations/recovery' },
      ] },
      { text: 'リファレンス', items: [
        { text: 'CLI', link: '/reference/cli' },
        { text: 'Configuration', link: '/reference/configuration' },
        { text: '制約', link: '/reference/limitations' },
      ] },
    ],
    socialLinks: [{ icon: 'github', link: 'https://github.com/kamichidu/go-nando' }],
    footer: { message: '設計の正本は `docs/design.ir.yaml`、実際の動作は実装で確認してください。' },
  },
})
