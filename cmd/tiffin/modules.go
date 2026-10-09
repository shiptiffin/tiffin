package main

// Every box module, registered by import. Add new modules here.
import (
	_ "github.com/shiptiffin/tiffin/internal/mod/analytics"
	_ "github.com/shiptiffin/tiffin/internal/mod/auth"
	_ "github.com/shiptiffin/tiffin/internal/mod/backup"
	_ "github.com/shiptiffin/tiffin/internal/mod/base"
	_ "github.com/shiptiffin/tiffin/internal/mod/box"
	_ "github.com/shiptiffin/tiffin/internal/mod/budget"
	_ "github.com/shiptiffin/tiffin/internal/mod/domains"
	_ "github.com/shiptiffin/tiffin/internal/mod/email"
	_ "github.com/shiptiffin/tiffin/internal/mod/managed"
	_ "github.com/shiptiffin/tiffin/internal/mod/monitor"
	_ "github.com/shiptiffin/tiffin/internal/mod/observe"
	_ "github.com/shiptiffin/tiffin/internal/mod/portable"
	_ "github.com/shiptiffin/tiffin/internal/mod/postgres"
	_ "github.com/shiptiffin/tiffin/internal/mod/protect"
	_ "github.com/shiptiffin/tiffin/internal/mod/queue"
	_ "github.com/shiptiffin/tiffin/internal/mod/runtime"
	_ "github.com/shiptiffin/tiffin/internal/mod/storage"
	_ "github.com/shiptiffin/tiffin/internal/mod/update"
	_ "github.com/shiptiffin/tiffin/internal/mod/valkey"
)
