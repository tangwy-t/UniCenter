package config

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	Server        ServerConfig        `mapstructure:"server"`
	Database      DatabaseConfig      `mapstructure:"database"`
	Redis         RedisConfig         `mapstructure:"redis"`
	Snowflake     SnowflakeConfig     `mapstructure:"snowflake"`
	Log           LogConfig           `mapstructure:"log"`
	Storage       StorageConfig       `mapstructure:"storage"`
	Observability ObservabilityConfig `mapstructure:"observability"`
}

// StorageConfig 文件对象存储后端配置（静态配置；S3 等凭据建议走环境变量注入，
// 避免明文落盘或写入数据库热配置）。
type StorageConfig struct {
	Backend string   `mapstructure:"backend"` // local（本地盘，默认） | s3（S3 兼容）
	S3      S3Config `mapstructure:"s3"`
}

// S3Config S3 兼容对象存储连接参数。
type S3Config struct {
	Endpoint  string `mapstructure:"endpoint"`
	AccessKey string `mapstructure:"accessKey"`
	SecretKey string `mapstructure:"secretKey"`
	Bucket    string `mapstructure:"bucket"`
	Region    string `mapstructure:"region"`
	UseSSL    bool   `mapstructure:"useSSL"`
	PathStyle bool   `mapstructure:"pathStyle"`
}

type ServerConfig struct {
	Port           int    `mapstructure:"port"`
	Mode           string `mapstructure:"mode"`
	APIPrefix      string `mapstructure:"apiPrefix"`
	ReadTimeout    int    `mapstructure:"readTimeout"`    // HTTP 读取超时（秒）
	WriteTimeout   int    `mapstructure:"writeTimeout"`   // HTTP 写入超时（秒）
	IdleTimeout    int    `mapstructure:"idleTimeout"`    // HTTP 空闲连接超时（秒）
	MaxHeaderBytes int    `mapstructure:"maxHeaderBytes"` // 最大请求头大小（字节）
	DrainTimeout   int    `mapstructure:"drainTimeout"`   // drain 阶段超时（秒）
	CleanupTimeout int    `mapstructure:"cleanupTimeout"` // cleanup 阶段超时（秒）
	// TrustedProxies 是受信任的反向代理地址列表（CIDR/IP）。
	// 为空时不信任任何代理，ClientIP 直接取对端地址 —— 防止 X-Forwarded-For
	// 伪造绕过基于 IP 的登录限流与验证码频控。
	TrustedProxies []string `mapstructure:"trustedProxies"`
}

type DatabaseConfig struct {
	Driver              string `mapstructure:"driver"` // mysql（默认） | postgres
	Host                string `mapstructure:"host"`
	Port                int    `mapstructure:"port"`
	User                string `mapstructure:"user"`
	Password            string `mapstructure:"password"`
	DBName              string `mapstructure:"dbname"`
	Charset             string `mapstructure:"charset"`
	SSLMode             string `mapstructure:"sslmode"` // postgres: disable|require|verify-ca|verify-full
	Schema              string `mapstructure:"schema"`  // postgres: 目标 schema/search_path（默认 public，可逗号分隔多个；MySQL 忽略）
	MaxIdleConns        int    `mapstructure:"maxIdleConns"`
	MaxOpenConns        int    `mapstructure:"maxOpenConns"`
	MaxLifetime         int    `mapstructure:"maxLifetime"`
	SlowQueryThreshold  int    `mapstructure:"slowQueryThreshold"`
	SlowQueryLogEnabled bool   `mapstructure:"slowQueryLogEnabled"`
	StatsInterval       int    `mapstructure:"statsInterval"`
}

type RedisConfig struct {
	Mode         string   `mapstructure:"mode"`         // single | cluster | sentinel
	Addr         string   `mapstructure:"addr"`         // 单机地址 (mode=single)
	Addrs        []string `mapstructure:"addrs"`        // 集群地址列表 (mode=cluster)
	MasterName   string   `mapstructure:"masterName"`   // 哨兵主节点名 (mode=sentinel)
	Password     string   `mapstructure:"password"`     // 密码
	DB           int      `mapstructure:"db"`           // 数据库编号 (仅 single 模式)
	DialTimeout  int      `mapstructure:"dialTimeout"`  // 连接超时（毫秒），默认 5000
	ReadTimeout  int      `mapstructure:"readTimeout"`  // 读取超时（毫秒），默认 3000
	WriteTimeout int      `mapstructure:"writeTimeout"` // 写入超时（毫秒），默认 3000
}

type SnowflakeConfig struct {
	WorkerID int64 `mapstructure:"workerId"`
}

type LogConfig struct {
	Level       string `mapstructure:"level"`       // debug | info | warn | error
	Format      string `mapstructure:"format"`      // json | console
	Output      string `mapstructure:"output"`      // stdout | file
	FilePath    string `mapstructure:"filePath"`    // log file path (when output=file)
	MaxSize     int    `mapstructure:"maxSize"`     // max megabytes before rotation
	MaxBackups  int    `mapstructure:"maxBackups"`  // max number of old log files
	MaxAge      int    `mapstructure:"maxAge"`      // max days to retain old log files
	Compress    bool   `mapstructure:"compress"`    // compress rotated files
	Caller      bool   `mapstructure:"caller"`      // show caller in log
	Development bool   `mapstructure:"development"` // development mode
}

// ObservabilityConfig 可观测性配置。
// 注:pprof 的启停与自动关闭时长不走此文件,而是 DB 配置键
// sys.pprof.enabled / sys.pprof.autoOffSeconds(可热更,见 PprofHandler)。
type ObservabilityConfig struct {
	Tracing TracingConfig `mapstructure:"tracing"`
}

// TracingConfig OpenTelemetry 分布式追踪配置。
type TracingConfig struct {
	Enabled    bool    `mapstructure:"enabled"`
	Endpoint   string  `mapstructure:"endpoint"`
	SampleRate float64 `mapstructure:"sampleRate"`
	// ServiceName 上报给 collector 的服务标识;留空时回退到模块名,
	// 避免硬编码与仓库身份脱节。
	ServiceName string `mapstructure:"serviceName"`
}

func Load() (*Config, error) {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath("./configs")
	viper.AddConfigPath(".")
	// 环境变量覆盖:database.password → DATABASE_PASSWORD。
	// 没有 replacer 时 AutomaticEnv 只认 "DATABASE.PASSWORD" 这种带点的
	// 环境变量名 —— 标准环境里根本设置不了,等于死代码,密钥只能写进
	// 配置文件。激活后 docker/k8s 部署可用 env 注入 DB/Redis 凭据,
	// 配置文件无需携带敏感值。
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	return &cfg, nil
}

// DriverName 归一化驱动名：未配置（空串）视为 mysql 以兼容既有部署。
// postgres 的别名 postgresql / pg 一并归一为 postgres；其余原样返回，
// 由调用方显式报错，避免静默回退到错误的驱动。
func (d DatabaseConfig) DriverName() string {
	switch strings.ToLower(strings.TrimSpace(d.Driver)) {
	case "", "mysql":
		return "mysql"
	case "postgres", "postgresql", "pg":
		return "postgres"
	default:
		return strings.ToLower(strings.TrimSpace(d.Driver))
	}
}

// DSN 依据 DriverName 返回对应驱动的连接串。
func (d DatabaseConfig) DSN() string {
	if d.DriverName() == "postgres" {
		return d.postgresDSN()
	}
	return d.mysqlDSN()
}

// mysqlDSN 返回 go-sql-driver/mysql 格式的 DSN。
// sql_mode=PIPES_AS_CONCAT 让 MySQL 的 || 表现为标准 SQL 的字符串拼接，
// 与 PostgreSQL / SQLite 原生行为对齐（见 repository/dept.go、user.go 的 ancestors 查询）。
func (d DatabaseConfig) mysqlDSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=Local&sql_mode=PIPES_AS_CONCAT",
		d.User, d.Password, d.Host, d.Port, d.DBName, d.Charset)
}

// postgresDSN 返回 pgx 可解析的 URL 形式连接串。
// 用 url.URL 组装以保证密码含空格 / @ / : 等特殊字符时被正确转义，
// 避免手拼 key=value 时值被空白或引号截断。sslmode 默认 disable（本地/内网免证书）。
func (d DatabaseConfig) postgresDSN() string {
	sslmode := "disable"
	if strings.TrimSpace(d.SSLMode) != "" {
		sslmode = strings.TrimSpace(d.SSLMode)
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(d.User, d.Password),
		Host:   net.JoinHostPort(d.Host, strconv.Itoa(d.Port)),
		Path:   "/" + d.DBName,
	}
	q := u.Query()
	q.Set("sslmode", sslmode)
	if strings.TrimSpace(d.Schema) != "" {
		// search_path 决定 GORM AutoMigrate / 查询落在哪个 schema，等价于 SET search_path。
		// 与 DSN 顶层的 schema 无关，pgx 会把未知 query 参数作为连接 startup 参数发送。
		q.Set("search_path", strings.TrimSpace(d.Schema))
	}
	u.RawQuery = q.Encode()
	return u.String()
}
