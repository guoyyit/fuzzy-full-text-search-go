// fsearch 是本地全文检索服务的可执行入口。
//
// 用法：
//
//	fsearch -addr :8080 -data ./data -config config.json
//
// 不提供 -config 时使用内置默认配置（容错上限 1、默认字段权重 1、
// 容错惩罚 0.3、短语加成 0.5）。
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"

	"github.com/guoyyit/fuzzy-full-text-search-go/internal/config"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/server"
	"github.com/guoyyit/fuzzy-full-text-search-go/internal/service"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP 监听地址")
	data := flag.String("data", "./fsearch-data", "本地数据目录")
	configPath := flag.String("config", "", "配置文件路径（JSON）；为空使用默认配置")
	flag.Parse()

	cfg := config.Default()
	if *configPath != "" {
		raw, err := os.ReadFile(*configPath)
		if err != nil {
			log.Fatalf("读取配置失败: %v", err)
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			log.Fatalf("解析配置失败: %v", err)
		}
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("配置非法: %v", err)
	}

	svc, err := service.Open(*data, cfg)
	if err != nil {
		log.Fatalf("打开服务失败: %v", err)
	}
	defer func() {
		if err := svc.Close(); err != nil {
			log.Printf("关闭服务出错: %v", err)
		}
	}()

	srv := server.New(svc)
	log.Printf("fsearch 监听 %s，数据目录 %s", *addr, *data)
	if err := http.ListenAndServe(*addr, srv.Routes()); err != nil {
		log.Fatal(err)
	}
}
