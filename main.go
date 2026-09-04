package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:dashboard/dist
var assets embed.FS

func main() {
	app := NewApp(assets)

	err := wails.Run(&options.App{
		Title:     "Telegram DL",
		Width:     1280,
		Height:    800,
		MinWidth:  950,
		MinHeight: 680,
		AssetServer: &assetserver.Options{
			Assets:  assets,
			Handler: app.Handler(),
		},
		BackgroundColour: &options.RGBA{R: 7, G: 17, B: 31, A: 255},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []any{
			app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			BackdropType:         windows.Mica,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
