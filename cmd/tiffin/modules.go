package main

// Every box module, registered by import. Add new modules here.
import (
	_ "github.com/btahir/tiffin/internal/mod/analytics"
	_ "github.com/btahir/tiffin/internal/mod/auth"
	_ "github.com/btahir/tiffin/internal/mod/backup"
	_ "github.com/btahir/tiffin/internal/mod/base"
	_ "github.com/btahir/tiffin/internal/mod/box"
	_ "github.com/btahir/tiffin/internal/mod/budget"
	_ "github.com/btahir/tiffin/internal/mod/domains"
	_ "github.com/btahir/tiffin/internal/mod/email"
	_ "github.com/btahir/tiffin/internal/mod/observe"
	_ "github.com/btahir/tiffin/internal/mod/portable"
	_ "github.com/btahir/tiffin/internal/mod/postgres"
	_ "github.com/btahir/tiffin/internal/mod/protect"
	_ "github.com/btahir/tiffin/internal/mod/queue"
	_ "github.com/btahir/tiffin/internal/mod/runtime"
	_ "github.com/btahir/tiffin/internal/mod/storage"
	_ "github.com/btahir/tiffin/internal/mod/update"
	_ "github.com/btahir/tiffin/internal/mod/valkey"
)
