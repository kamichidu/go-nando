import { defineConfig } from 'vitepress'
import { withMermaid } from 'vitepress-plugin-mermaid'

export default withMermaid(defineConfig({
  lang: 'en-US',
  title: 'nando Manual',
  description: 'Installation, usage, and operations guide for nando',
  base: '/go-nando/',
  cleanUrls: true,
  themeConfig: {
    nav: [
      { text: 'Getting started', link: '/getting-started/install' },
      { text: 'Setup', link: '/setup/repository' },
      { text: 'Use', link: '/use/fuse' },
      { text: 'Administration', link: '/use/cli' },
      { text: 'Operations', link: '/operations/daemon' },
      { text: 'Reference', link: '/reference/cli' },
    ],
    sidebar: [
      { text: 'Overview', items: [
        { text: 'About nando', link: '/' },
        { text: 'Architecture', link: '/getting-started/concepts' },
      ] },
      { text: 'Getting started', items: [
        { text: 'Install', link: '/getting-started/install' },
        { text: 'Quick start', link: '/getting-started/quickstart' },
        { text: 'Versions and updates', link: '/getting-started/versioning' },
      ] },
      { text: 'Setup', items: [
        { text: 'Initialize a repository', link: '/setup/repository' },
        { text: 'Configuration file', link: '/setup/configuration' },
        { text: 'Google Cloud Storage', link: '/setup/gcs' },
        { text: 'S3-compatible storage', link: '/setup/s3' },
        { text: 'systemd system service', link: '/setup/systemd' },
      ] },
      { text: 'Use', items: [
        { text: 'FUSE', link: '/use/fuse' },
        { text: 'WebDAV', link: '/use/webdav' },
        { text: 'Remote delivery', link: '/use/remote-delivery' },
      ] },
      { text: 'Administration', items: [
        { text: 'CLI', link: '/use/cli' },
      ] },
      { text: 'Operations', items: [
        { text: 'daemon', link: '/operations/daemon' },
        { text: 'Garbage collection', link: '/operations/gc' },
        { text: 'Backup and restore', link: '/operations/backup-restore' },
        { text: 'Recovery', link: '/operations/recovery' },
      ] },
      { text: 'Reference', items: [
        { text: 'CLI', link: '/reference/cli' },
        { text: 'Configuration', link: '/reference/configuration' },
        { text: 'Limitations', link: '/reference/limitations' },
      ] },
    ],
    socialLinks: [{ icon: 'github', link: 'https://github.com/kamichidu/go-nando' }],
    footer: { message: 'The design authority is `docs/design.ir.yaml`; consult the implementation for actual behavior.' },
  },
}))
