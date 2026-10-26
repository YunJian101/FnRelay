package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func main() {
	dataDir := flag.String("data", envOr("FNRELAY_DATA", "./data"), "数据目录（配置、证书、日志）")
	adminPort := flag.Int("admin-port", envInt("FNRELAY_ADMIN_PORT", 0), "本地开发回退端口（仅绑定 127.0.0.1，0=关闭）")
	gatewaySocket := flag.String("gateway-socket", envOr("FNRELAY_GATEWAY_SOCKET", ""), "统一网关 Unix Socket 路径（空则关闭）")
	gatewayPrefix := flag.String("gateway-prefix", envOr("FNRELAY_GATEWAY_PREFIX", "/app/fnrelay"), "统一网关 URL 前缀")
	showVersion := flag.Bool("version", false, "显示版本号")
	flag.Parse()

	if *showVersion {
		fmt.Printf("FnRelay %s (%s)\n", Version, BuildTime)
		return
	}

	abs, err := filepath.Abs(*dataDir)
	if err != nil {
		abs = *dataDir
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		appLog.Fatalf("无法创建数据目录 %s: %v", abs, err)
	}
	cfgPath := filepath.Join(abs, "config.json")
	cfg, existed, err := LoadConfig(cfgPath)
	if err != nil {
		appLog.Fatalf("读取配置失败: %v", err)
	}
	if !existed {
		cfg.Normalize()
		if err := SaveConfig(cfgPath, cfg); err != nil {
			appLog.Fatalf("写入默认配置失败: %v", err)
		}
	}
	app := NewApp(cfg, cfgPath, abs, *adminPort)
	// 只有显式打开本地回退端口时才跳过飞牛网关鉴权，供开发/截图使用。
	app.devMode = *adminPort > 0
	appLog.Printf("FnRelay %s 启动中，数据目录 %s", Version, abs)

	if err := app.proxy.Start(cfg); err != nil {
		if !existed && cfg.Proxy.Port == 443 {
			fb := cfg.Clone()
			fb.Proxy.Port = 8443
			fb.Proxy.AutoFallback = true
			if saveErr := SaveConfig(cfgPath, fb); saveErr == nil {
				app.setConfig(fb)
				if startErr := app.proxy.Start(fb); startErr == nil {
					app.autoFallback = "443 端口被飞牛系统占用，已自动改用 8443。可在「设置」中改成其他端口。"
					app.logf("警告", "%s", app.autoFallback)
				} else {
					app.autoFallback = "443 与 8443 端口都无法监听，请在「设置」中更换代理端口。"
					app.logf("错误", "%s", app.autoFallback)
				}
			}
		} else {
			app.autoFallback = err.Error()
		}
	}

	app.StartBackground()

	adminHandler := app.AdminHandler()

	var devSrv *http.Server
	if *adminPort > 0 {
		devSrv = &http.Server{
			Handler:           app.WithGateway(adminHandler, *gatewayPrefix, false),
			ReadHeaderTimeout: 20 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20,
		}
		ln, listenErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *adminPort))
		if listenErr != nil {
			appLog.Fatalf("本地回退端口 %d 监听失败: %v", *adminPort, listenErr)
		}
		go func() {
			appLog.Printf("本地开发回退: http://127.0.0.1:%d%s", *adminPort, *gatewayPrefix)
			if err := devSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				appLog.Printf("本地回退退出: %v", err)
			}
		}()
	}

	var gatewaySrv *http.Server
	if *gatewaySocket != "" {
		_ = os.Remove(*gatewaySocket)
		uln, listenErr := net.Listen("unix", *gatewaySocket)
		if listenErr != nil {
			appLog.Printf("统一网关 socket 监听失败: %v", listenErr)
		} else {
			_ = os.Chmod(*gatewaySocket, 0o666)
			gatewaySrv = &http.Server{
				Handler:           app.WithGateway(adminHandler, *gatewayPrefix, true),
				ReadHeaderTimeout: 20 * time.Second,
				IdleTimeout:       120 * time.Second,
				MaxHeaderBytes:    1 << 20,
			}
			go func() {
				appLog.Printf("统一网关: %s -> %s", *gatewayPrefix, *gatewaySocket)
				if err := gatewaySrv.Serve(uln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					appLog.Printf("统一网关退出: %v", err)
				}
			}()
		}
	}
	if gatewaySrv == nil && devSrv == nil {
		appLog.Fatal("没有可用的管理入口：需要 --gateway-socket 或 --admin-port")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	appLog.Println("收到退出信号，正在停止 FnRelay ...")
	app.proxy.Stop()
	app.Close()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if devSrv != nil {
		_ = devSrv.Shutdown(shutCtx)
	}
	if gatewaySrv != nil {
		_ = gatewaySrv.Shutdown(shutCtx)
	}
	if *gatewaySocket != "" {
		_ = os.Remove(*gatewaySocket)
	}
	appLog.Println("FnRelay 已停止")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
