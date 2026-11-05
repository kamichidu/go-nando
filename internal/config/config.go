// Package config preserves the historical internal import path while keeping
// runtimeconfig as the single configuration implementation.
package config

import "github.com/kamichidu/go-nando/runtimeconfig"

type Config = runtimeconfig.Config
type RepositoryConfig = runtimeconfig.RepositoryConfig
type LoggingConfig = runtimeconfig.LoggingConfig
type RemoteConfig = runtimeconfig.RemoteConfig
type DaemonConfig = runtimeconfig.DaemonConfig
type RecoveryConfig = runtimeconfig.RecoveryConfig
type FuseConfig = runtimeconfig.FuseConfig
type WebDAVConfig = runtimeconfig.WebDAVConfig
type RemoteSchedulerSettings = runtimeconfig.RemoteSchedulerSettings
type ResolvedPaths = runtimeconfig.ResolvedPaths

var LoadConfig = runtimeconfig.LoadConfig
var LoadOptionalConfig = runtimeconfig.Load
var DefaultPaths = runtimeconfig.DefaultPaths
var ResolvePaths = runtimeconfig.ResolvePaths
