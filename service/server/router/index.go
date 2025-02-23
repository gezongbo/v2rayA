package router

import (
	"embed"
	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/gookit/color"
	"github.com/v2rayA/v2rayA/common"
	"github.com/v2rayA/v2rayA/db/configure"
	"github.com/v2rayA/v2rayA/global"
	"github.com/v2rayA/v2rayA/server/controller"
	"github.com/v2rayA/v2rayA/server/router/jwt"
	"github.com/v2rayA/v2rayA/server/router/reqCache"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
)

//go:embed web
var webRoot embed.FS

// relativeFS implements fs.FS
type relativeFS struct {
	root        fs.FS
	relativeDir string
}

func (c relativeFS) Open(name string) (fs.File, error) {
	return c.root.Open(path.Join(c.relativeDir, name))
}

func ServeGUI(engine *gin.Engine) {
	r := engine.Use(gzip.Gzip(gzip.DefaultCompression))
	webDir := global.GetEnvironmentConfig().WebDir
	if webDir == "" {
		webFS := relativeFS{
			root:        webRoot,
			relativeDir: "web",
		}
		fs.WalkDir(webFS, "/", func(path string, info fs.DirEntry, err error) error {
			if path == "/" {
				return nil
			}
			if info.IsDir() {
				r.StaticFS("/"+info.Name(), http.FS(relativeFS{
					root:        webFS,
					relativeDir: path,
				}))
				return filepath.SkipDir
			}
			r.GET("/"+info.Name(), func(ctx *gin.Context) {
				ctx.FileFromFS(path, http.FS(webFS))
			})
			return nil
		})
		r.GET("/", func(ctx *gin.Context) {
			f, err := webFS.Open("index.html")
			if err != nil {
				ctx.Status(400)
				return
			}
			defer f.Close()
			b, err := io.ReadAll(f)
			if err != nil {
				ctx.Status(400)
				return
			}
			ctx.Header("Content-Type", "text/html; charset=utf-8")
			ctx.String(http.StatusOK, string(b))
		})
	} else {
		if _, err := os.Stat(webDir); os.IsNotExist(err) {
			log.Printf("[Warning] web files cannot be found at %v. web UI cannot be served", webDir)
		} else {
			filepath.Walk(webDir, func(path string, info os.FileInfo, err error) error {
				if path == webDir {
					return nil
				}
				if info.IsDir() {
					r.Static("/"+info.Name(), path)
					return filepath.SkipDir
				}
				r.StaticFile("/"+info.Name(), path)
				return nil
			})
			engine.LoadHTMLFiles(path.Join(webDir, "index.html"))
			r.GET("/", func(context *gin.Context) {
				context.HTML(http.StatusOK, "index.html", nil)
			})
		}
	}

	app := global.GetEnvironmentConfig()

	ip, port, _ := net.SplitHostPort(app.Address)
	addrs, err := net.InterfaceAddrs()
	if net.ParseIP(ip).IsUnspecified() && err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				printRunningAt("http://" + net.JoinHostPort(ipnet.IP.String(), port))
			}
		}
	} else {
		printRunningAt("http://" + app.Address)
	}
}

func nocache(c *gin.Context) {
	c.Header("Cache-Control", "no-cache, no-store, max-age=0, must-revalidate")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
}

func Run() error {
	engine := gin.New()
	//ginpprof.Wrap(engine)
	engine.Use(gin.Recovery())
	corsConfig := cors.DefaultConfig()
	corsConfig.AllowAllOrigins = true
	corsConfig.AllowMethods = []string{
		"GET", "POST", "DELETE", "PUT", "PATCH", "OPTIONS", "HEAD",
	}
	corsConfig.AllowWebSockets = true
	corsConfig.AllowCredentials = true
	corsConfig.AddAllowHeaders("Authorization", common.RequestIdHeader)
	engine.Use(cors.New(corsConfig))
	noAuth := engine.Group("api",
		nocache,
		reqCache.ReqCache,
	)
	{
		noAuth.GET("version", controller.GetVersion)
		noAuth.POST("login", controller.PostLogin)
		noAuth.POST("account", controller.PostAccount)
	}
	auth := engine.Group("api",
		nocache,
		func(ctx *gin.Context) {
			if !configure.HasAnyAccounts() {
				common.Response(ctx, common.UNAUTHORIZED, gin.H{
					"first": true,
				})
				ctx.Abort()
				return
			}
		},
		jwt.JWTAuth(false),
		reqCache.ReqCache,
	)
	{
		auth.POST("import", controller.PostImport)
		auth.GET("touch", controller.GetTouch)
		auth.DELETE("touch", controller.DeleteTouch)
		auth.POST("connection", controller.PostConnection)
		auth.DELETE("connection", controller.DeleteConnection)
		auth.POST("v2ray", controller.PostV2ray)
		auth.DELETE("v2ray", controller.DeleteV2ray)
		auth.GET("pingLatency", controller.GetPingLatency)
		auth.GET("httpLatency", controller.GetHttpLatency)
		auth.GET("sharingAddress", controller.GetSharingAddress)
		auth.GET("remoteGFWListVersion", controller.GetRemoteGFWListVersion)
		auth.GET("setting", controller.GetSetting)
		auth.PUT("setting", controller.PutSetting)
		auth.PUT("gfwList", controller.PutGFWList)
		auth.PUT("subscription", controller.PutSubscription)
		auth.PATCH("subscription", controller.PatchSubscription)
		auth.GET("ports", controller.GetPorts)
		auth.PUT("ports", controller.PutPorts)
		//auth.PUT("account", controller.PutAccount)
		auth.GET("portWhiteList", controller.GetPortWhiteList)
		auth.PUT("portWhiteList", controller.PutPortWhiteList)
		auth.POST("portWhiteList", controller.PostPortWhiteList)
		auth.GET("dnsList", controller.GetDnsList)
		auth.PUT("dnsList", controller.PutDnsList)
		auth.GET("routingA", controller.GetRoutingA)
		auth.PUT("routingA", controller.PutRoutingA)
		auth.GET("outbounds", controller.GetOutbounds)
		auth.POST("outbound", controller.PostOutbound)
		auth.DELETE("outbound", controller.DeleteOutbound)
		auth.GET("message", controller.WsMessage)
	}

	ServeGUI(engine)

	return engine.Run(global.GetEnvironmentConfig().Address)
}

func printRunningAt(address string) {
	color.Red.Println("v2rayA is listening at", address)
}
