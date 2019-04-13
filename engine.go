package engine

import (
	"compress/gzip"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/globalsign/mgo"
	"github.com/go-redis/redis"
	"github.com/otamoe/gin-engine/compress"
	"github.com/otamoe/gin-engine/errors"
	"github.com/otamoe/gin-engine/logger"
	"github.com/otamoe/gin-engine/mongo"
	"github.com/otamoe/gin-engine/notfound"
	ginRedis "github.com/otamoe/gin-engine/redis"
	"github.com/otamoe/gin-engine/size"
	mgoModel "github.com/otamoe/mgo-model"
	"github.com/sirupsen/logrus"
)

type (
	CompressConfig struct {
		Types []string `json:"types,omitempty"`
	}

	LoggerConfig struct {
		File string `json:"file,omitempty"`
	}

	RedisConfig struct {
		URLs []string `json:"urls,omitempty"`

		PoolLimit   int           `json:"pool_limit,omitempty"`
		PoolTimeout time.Duration `json:"pool_timeout,omitempty"`

		DialTimeout   time.Duration `json:"dial_timeout,omitempty"`
		SocketTimeout time.Duration `json:"socket_timeout,omitempty"`
	}

	MongoConfig struct {
		URLs []string `json:"urls,omitempty"`

		PoolLimit   int           `json:"pool_limit,omitempty"`
		PoolTimeout time.Duration `json:"pool_timeout,omitempty"`

		DialTimeout   time.Duration `json:"dial_timeout,omitempty"`
		SocketTimeout time.Duration `json:"socket_timeout,omitempty"`
	}

	ServerConfig struct {
		Addr              string        `json:"addr,omitempty"`
		Certificates      []Certificate `json:"certificates,omitempty"`
		ReadTimeout       time.Duration `json:"read_timeout,omitempty"`
		ReadHeaderTimeout time.Duration `json:"read_header_timeout,omitempty"`
		WriteTimeout      time.Duration `json:"write_timeout,omitempty"`
		IdleTimeout       time.Duration `json:"idle_timeout,omitempty"`
	}

	Certificate struct {
		Certificate string `json:"certificate"`
		PrivateKey  string `json:"private_key"`
	}

	Handler map[string]http.Handler

	Engine struct {
		ENV  string `json:"env,omitempty"`
		Name string `json:"name,omitempty"`

		CompressConfig CompressConfig `json:"compress,omitempty"`
		LoggerConfig   LoggerConfig   `json:"logger,omitempty"`
		RedisConfig    RedisConfig    `json:"redis,omitempty"`
		MongoConfig    MongoConfig    `json:"mongo,omitempty"`
		ServerConfig   ServerConfig   `json:"server,omitempty"`

		Handler Handler `json:"-"`

		mongoSession *mgo.Session
	}
)

func (engine *Engine) Init() *Engine {
	switch engine.ENV {
	case "dev":
		engine.ENV = "development"
	case "test":
		engine.ENV = "test"
	default:
		engine.ENV = "production"
	}
	if engine.Name == "" {
		dir, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		dir, engine.Name = path.Split(strings.Trim(dir, "/\\"))
		if engine.Name == "" {
			engine.Name = "unnamed"
		}
		engine.Name = strings.ToLower(engine.Name)
	}
	engine.initGin()
	engine.initCompress()
	engine.initLogger()
	engine.initRedis()
	engine.initMongo()
	engine.initServer()

	return engine
}
func (engine *Engine) initGin() {
	switch engine.ENV {
	case "development":
		gin.SetMode(gin.DebugMode)
	case "test":
		gin.SetMode(gin.TestMode)
	default:
		gin.SetMode(gin.ReleaseMode)
	}
}

func (engine *Engine) initCompress() {
	config := engine.CompressConfig
	if config.Types == nil {
		config.Types = []string{"application/json", "text/plain"}
	}

	engine.CompressConfig = config
}

func (engine *Engine) initLogger() {
	switch engine.ENV {
	case "development":
		logrus.SetLevel(logrus.TraceLevel)
	case "test":
		logrus.SetLevel(logrus.TraceLevel)
	default:
		logrus.SetLevel(logrus.InfoLevel)
	}

	logrus.SetFormatter(&logrus.TextFormatter{
		FullTimestamp:   true,
		TimestampFormat: time.RFC3339,
	})

	logrus.SetOutput(os.Stdout)
	if engine.LoggerConfig.File != "" {
		writer, err := os.OpenFile(engine.LoggerConfig.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			panic(err)
		}
		logrus.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: time.RFC3339,
		})
		logrus.SetOutput(writer)
	}
	log.SetOutput(logrus.StandardLogger().Writer())
}

func (engine *Engine) initRedis() {
	config := engine.RedisConfig
	if len(config.URLs) == 0 {
		config.URLs = append(config.URLs, "localhost:6379")
	}
	if config.PoolLimit == 0 {
		config.PoolLimit = 2048
	}
	if config.PoolTimeout == 0 {
		config.PoolTimeout = time.Second * 3
	}
	if config.DialTimeout == 0 {
		config.DialTimeout = time.Second * 2
	}
	if config.SocketTimeout == 0 {
		config.SocketTimeout = time.Second * 2
	}

	engine.RedisConfig = config

	logWriter := engine.Logger().Writer()
	redis.SetLogger(log.New(logWriter, "", 0))
}

func (engine *Engine) initMongo() {
	config := engine.MongoConfig
	if len(config.URLs) == 0 {
		config.URLs = append(config.URLs, "localhost:27017/"+engine.Name)
	}
	if config.PoolLimit == 0 {
		config.PoolLimit = 2048
	}
	if config.PoolTimeout == 0 {
		config.PoolTimeout = time.Second * 3
	}
	if config.DialTimeout == 0 {
		config.DialTimeout = time.Second * 2
	}
	if config.SocketTimeout == 0 {
		config.SocketTimeout = time.Minute * 1
	}

	engine.MongoConfig = config

	mgoModel.CONTEXT = mongo.CONTEXT

	if engine.ENV == "development" {
		mgo.SetDebug(true)
		logWriter := engine.Logger().Writer()
		mgo.SetLogger(log.New(logWriter, "", 0))
	}

	var err error
	engine.mongoSession, err = mgo.DialWithTimeout(strings.Join(config.URLs, ","), config.DialTimeout)
	if err != nil {
		panic(err)
	}
	engine.mongoSession.SetPoolLimit(config.PoolLimit)
	engine.mongoSession.SetPoolTimeout(config.PoolTimeout)
	engine.mongoSession.SetSocketTimeout(config.SocketTimeout)
}

func (engine *Engine) initServer() {
	config := engine.ServerConfig
	if config.Addr == "" {
		if config.Certificates == nil {
			config.Addr = ":8080"
		} else {
			config.Addr = ":8443"
		}
	}
	if strings.HasSuffix(config.Addr, ":443") || strings.HasSuffix(config.Addr, ":8443") || (config.Certificates != nil && len(config.Certificates) == 0) {
		if len(config.Certificates) == 0 {
			for host := range engine.Handler {
				priv, cert, err := NewCertificate(host, []string{host}, "ecdsa", 384)
				if err != nil {
					panic(err)
				}

				privBytes, err2 := x509.MarshalECPrivateKey(priv.(*ecdsa.PrivateKey))
				if err2 != nil {
					panic(err)
				}

				privBlock := &pem.Block{
					Type:  "EC PRIVATE KEY",
					Bytes: privBytes,
				}
				privPem := pem.EncodeToMemory(privBlock)

				certBlock := &pem.Block{
					Type:  "CERTIFICATE",
					Bytes: cert,
				}

				certPem := pem.EncodeToMemory(certBlock)

				config.Certificates = append(config.Certificates, Certificate{
					Certificate: string(certPem),
					PrivateKey:  string(privPem),
				})
			}
		}
	}

	if config.ReadTimeout == 0 {
		config.ReadTimeout = time.Second * 20
	}
	if config.ReadHeaderTimeout == 0 {
		config.ReadHeaderTimeout = time.Second * 10
	}
	if config.WriteTimeout == 0 {
		config.WriteTimeout = time.Second * 30
	}
	if config.IdleTimeout == 0 {
		config.IdleTimeout = time.Second * 300
	}

	engine.ServerConfig = config
}

func (engine *Engine) Logger() *logrus.Logger {
	return logrus.StandardLogger()
}

func (engine *Engine) Redis() (client *redis.Client) {
	client = redis.NewClient(&redis.Options{
		Addr:         strings.Join(engine.RedisConfig.URLs, ","),
		DialTimeout:  engine.RedisConfig.DialTimeout,
		ReadTimeout:  engine.RedisConfig.SocketTimeout,
		WriteTimeout: engine.RedisConfig.SocketTimeout,
		PoolSize:     engine.RedisConfig.PoolLimit,
		PoolTimeout:  engine.RedisConfig.PoolTimeout,
	})
	return
}

func (engine *Engine) Mongo() (session *mgo.Session) {
	session = engine.mongoSession.Clone()
	return
}

func (engine *Engine) New() (r *gin.Engine) {
	r = gin.New()

	// Compress 中间件
	r.Use(compress.Middleware(compress.Config{
		GzipLevel: gzip.DefaultCompression,
		MinLength: 256,
		BrLGWin:   19,
		BrQuality: 6,
		Types:     engine.CompressConfig.Types,
	}))

	// logger
	r.Use(logger.Middleware(logger.Config{
		Prefix: "[HTTP] ",
	}))

	// errors
	r.Use(errors.Middleware())

	// Redis 中间件
	r.Use(ginRedis.Middleware(engine.Redis))

	// Mongo 中间件
	r.Use(mongo.Middleware(engine.Mongo))

	// body size
	r.Use(size.Middleware(1024 * 512))

	// 未匹配
	r.NoRoute(notfound.Middleware())

	return
}

func (engine *Engine) Server() {
	config := engine.ServerConfig

	var tlsConfig *tls.Config
	if len(config.Certificates) != 0 {
		var certificates []tls.Certificate
		for _, val := range config.Certificates {
			certificate, err := tls.X509KeyPair([]byte(val.Certificate), []byte(val.PrivateKey))
			if err != nil {
				panic(err)
			}
			certificates = append(certificates, certificate)
		}
		tlsConfig = &tls.Config{
			MinVersion:               tls.VersionTLS10,
			Certificates:             certificates,
			PreferServerCipherSuites: true,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			},
		}
		tlsConfig.BuildNameToCertificate()
	}

	logWriter := engine.Logger().Writer()
	defer logWriter.Close()

	server := http.Server{
		Addr:              config.Addr,
		Handler:           engine.Handler,
		TLSConfig:         tlsConfig,
		ReadTimeout:       config.ReadTimeout,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		WriteTimeout:      config.WriteTimeout,
		IdleTimeout:       config.IdleTimeout,
		MaxHeaderBytes:    4096,
		ErrorLog:          log.New(logWriter, "", 0),
	}

	// 执行
	go func() {
		var err error
		if tlsConfig == nil {
			err = server.ListenAndServe()
		} else {
			err = server.ListenAndServeTLS("", "")
		}
		if err != nil && err != http.ErrServerClosed {
			panic(err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server with
	// a timeout of 5 seconds.
	quit := make(chan os.Signal, 1)
	// kill (no param) default send syscanll.SIGTERM
	// kill -2 is syscall.SIGINT
	// kill -9 is syscall. SIGKILL but can"t be catch, so don't need add it
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutdown Server ...")
	//
	ctx, cancel := context.WithTimeout(context.Background(), config.ReadTimeout+config.WriteTimeout+config.ReadHeaderTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Panic("Server Shutdown:", err)
	}

	log.Println("Server exiting")
}

func (h Handler) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	switch req.URL.Path {
	case "/favicon.ico":
		writer.Header().Set("Content-Type", "image/x-icon")
		writer.WriteHeader(http.StatusOK)
		fmt.Fprintln(writer, "")
	case "/robots.txt":
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		fmt.Fprintln(writer, "Disallow: /")
	case "/crossdomain.xml":
		writer.Header().Set("Content-Type", "application/xml; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		fmt.Fprintln(writer, "<?xml version=\"1.0\"?><cross-domain-policy></cross-domain-policy>")
	default:
		var host string
		if host = req.Header.Get("X-Forwarded-Host"); host != "" {
		} else if host = req.Header.Get("X-Host"); host != "" {
		} else if host = req.Host; host != "" {
		} else if host = req.URL.Host; host != "" {
		} else {
			host = "localhost"
		}

		if host != "" {
			if index := strings.LastIndex(host, ":"); index != -1 {
				host = host[0:index]
			}
		}

		if mux := h[host]; mux != nil {
			mux.ServeHTTP(writer, req)
		} else if mux := h["default"]; mux != nil {
			mux.ServeHTTP(writer, req)
		} else {
			http.Error(writer, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		}
	}
}
func NewCertificate(name string, hosts []string, typ string, bits int) (priv crypto.PrivateKey, cert []byte, err error) {
	var pub crypto.PublicKey
	switch typ {
	case "ecdsa":
		{
			var privateKey *ecdsa.PrivateKey
			switch bits {
			case 224:
				privateKey, err = ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
			case 256:
				privateKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			case 384:
				privateKey, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
			case 521:
				privateKey, err = ecdsa.GenerateKey(elliptic.P521(), rand.Reader)
			}
			if err != nil {
				return
			}
			priv = privateKey
			pub = privateKey.Public()
		}
	default:
		{
			var privateKey *rsa.PrivateKey
			if privateKey, err = rsa.GenerateKey(rand.Reader, bits); err != nil {
				return
			}
			priv = privateKey
			pub = privateKey.Public()
		}
	}

	max := new(big.Int).Lsh(big.NewInt(1), 128)
	var serialNumber *big.Int
	if serialNumber, err = rand.Int(rand.Reader, max); err != nil {
		return
	}

	subject := pkix.Name{
		Organization:       []string{"Organization"},
		OrganizationalUnit: []string{"Organizational Unit"},
		CommonName:         name,
	}

	template := &x509.Certificate{
		SerialNumber:        serialNumber,
		Subject:             subject,
		NotBefore:           time.Now().Add(-(time.Hour * 24 * 30)),
		NotAfter:            time.Now().Add(time.Hour * 24 * 365 * 20),
		KeyUsage:            x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:         []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		PermittedDNSDomains: hosts,
		PermittedURIDomains: hosts,
	}

	if cert, err = x509.CreateCertificate(rand.Reader, template, template, pub, priv); err != nil {
		return
	}

	return
}
