## 配置读取模块

目的：此模块主要解决的问题是尽可能简单的加载配置文件以及AB测试中需要用到同类型不同数据的配置表。

思路：每一种不同类型的配置在模块中都会生成一个配置组，配置组中使用sync.Map管理实际的配置对象。获取配置的时候通过tag区分不同配置数据，如果获取不到对应tag配置，可以fallback到默认的主配置。

### 配置对象需要实现接口 IConfig
* 通过Name，Path定位配置源。
* ValuePtr传出配置对象的指针，用于json反序列化。
* OnLoad在反序列化完成后调用，用于初始化配置对象的数据。
```go
// =================== 数组类型配置表的例子 =========================
// 声明单个配置的结构和json标签
// 再声明管理配置文件的结构 把解析json数据的切片指针通过接口方法ValuePtr返回
// 除了IConfig接口的几个方法 可以自定义数据类型和方法来方便使用配置

type User struct {
	Name  string   `json:"name"`
	Age   int      `json:"age"`
	Likes []string `json:"likes"`
}

type UserConfig struct {
	Users   []*User          `json:"users"`
	UserMap map[string]*User `json:"userMap"`
}

func (u *UserConfig) Name() string {
	return "user"
}

func (u *UserConfig) Path() string {
	return "./json"
}

func (u *UserConfig) ValuePtr() any {
	u.Users = make([]*User, 0)
	return &u.Users
}

func (u *UserConfig) OnLoad() {
	u.UserMap = make(map[string]*User)
	for _, user := range u.Users {
		u.UserMap[user.Name] = user
	}
	log.Sugar.Info("load user config end")
}
```
```go
// =================== 结构体类型配置表的例子 =========================
// 和上述类似 可以直接使用声明的结构体来解析配置 也可以像上面一样定义一个管理配置文件的结构

type PriceConfig struct {
	Milk   int `json:"milk"`
	Cake   int `json:"cake"`
	Cheese int `json:"cheese"`
}

func (p *PriceConfig) Name() string {
	return "price"
}

func (p *PriceConfig) Path() string {
	return "./json"
}

func (p *PriceConfig) ValuePtr() any {
	return p
}

func (p *PriceConfig) OnLoad() {
	log.Sugar.Info("load price config end")
}
```

### 配置加载
* LoadConfig 加载本地配置文件 添加tag可选参数的话 会加载tag配置 如主配置为 user.json， tag为b的配置就为 user_b.json
* FocusConsulConfig 关注consul配置文件 通过监听consul的kv变化来动态更新配置 如有可匹配主配置Name的tag配置 会自动加载tag配置到对应配置组中
* ⚠️ 如果使用consul配置的话 需要在关注consul配置后再设置consul地址 因为需要通过关注列表筛选可用配置
```go
// 本地配置
config.LoadConfig(&UserConfig{})
config.LoadConfig(&PriceConfig{}, "b") // 加载主配置以及tag配置

// consul配置
config.FocusConsulConfig(&UserConfig{})
config.FocusConsulConfig(&PriceConfig{})
config.SetConsul("localhost:8500")
```

### 配置获取
* 通过泛型确定需要获取的配置类型
```go
// 获取主配置
userCfg := config.GetConfig[*UserConfig]()
// 获取tag配置 第二个参数为true时找不到tag配置时返回默认配置
priceTagCfg := config.GetConfigWithTag[*PriceConfig]("b", true)
```

### Consul 更新与停止

`FocusConsulConfig()` 仍需在 `SetConsul()` 前调用，并使用配置指针注册。`SetConsul()` 保持异步加载，返回时不代表首轮配置已经就绪；读取方需要处理配置尚未加载时的零值（指针类型为 `nil`）。

每个 `Path()` 对应独立的目录前缀，首尾斜杠会被统一处理（例如 `/dream`、`dream` 和 `/dream/` 均匹配 `dream/`）；空路径或 `/` 表示根目录。仅匹配该目录直属的 `.json` 配置。完整 `Name()` 可包含下划线，tag 文件格式仍为 `Name_tag.json`；有多个名称可匹配时，完整名称优先，其次选择最长的已注册名称。不同目录中的同名文件不会相互覆盖。

多前缀的 `OnLoad()` 和变更回调串行执行，可以在回调中读取配置或调用 `StopWatch()`。同一批更新保留 `IPriority` 的加载顺序；关联配置仍是逐个发布，不提供跨表原子快照。无效 JSON 保留上次有效值。

删除 KV 会移除对应的当前配置。删除 tag 后，`GetConfigWithTag(..., true)` 会回退到主配置；主配置也被删除时返回零值。既有配置指针不会被原地修改或失效，业务持有的旧引用需自行重新获取。现有 `OnConsulConfigChange()` 只通知成功加载的配置，不发送删除事件。

`StopWatch()` 取消已登记的监听，允许已经开始的回调完成；它不等待所有回调退出，因而可以在回调内调用。
