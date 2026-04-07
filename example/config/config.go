package main

import "github.com/murang/potato/log"

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

func (u *UserConfig) Priority() int {
	return 666
}

func (u *UserConfig) GetUserByName(name string) *User {
	return u.UserMap[name]
}

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
