package main

import (
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

func main() {
	initRunLogger()
	cleanupOldUpdateArtifacts()
	app := NewApp()
	handler, store, err := newBillingHandler()
	if err != nil {
		log.Fatal(err)
	}
	app.setStore(store)

	err = wails.Run(&options.App{
		Title:     "账单系统 v" + appVersion,
		Width:     1500,
		Height:    900,
		MinWidth:  480,
		MinHeight: 360,
		AssetServer: &assetserver.Options{
			Handler: handler,
		},
		BackgroundColour: &options.RGBA{R: 238, G: 244, B: 251, A: 1},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

func initRunLogger() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("WARN 获取程序路径失败：%v", err)
		return
	}
	logDir := filepath.Join(filepath.Dir(exe), "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Printf("WARN 创建日志目录失败：%v", err)
		return
	}
	name := time.Now().Format("20060102_150405") + ".log"
	f, err := os.OpenFile(filepath.Join(logDir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("WARN 创建日志文件失败：%v", err)
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Printf("INFO 程序启动 version=%s exe=%s", appVersion, exe)
}
