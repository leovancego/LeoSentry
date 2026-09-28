package activity

// Overview 是"今日概览"的数据快照，由服务端一次扫描 today.db 生成。
//
// 服务端只做必须在路由器上完成的部分：扫描明细、域名分类、按
// (设备, 时段, 应用) 聚合并计算去重后的活跃周期数；排序、筛选、多维组合、
// 图表渲染全部交给浏览器。四张事实表都是紧凑的整数数组，下标指向
// Categories / Apps / Devices，浏览器按需求和即可得到任意组合的结果：
//
//   - 同一设备的不同时段互不重叠，因此"某设备在某时段范围内的活跃时长"=
//     DeviceBuckets 中对应行的 Periods 之和，结果精确；
//   - 同理可对 CategoryBuckets / EntBuckets / AppBuckets 按任意时段求和；
//   - 同一时段内不同应用/类别可能同时活跃，不能把应用的周期数相加当作设备时长，
//     所以设备、类别、娱乐三个层级分别给出去重后的周期数。
type Overview struct {
	GeneratedAt int64 `json:"generatedAt"`
	// Which 为 today 或 yesterday。
	Which    string `json:"which"`
	Day      string `json:"day"`
	DayStart int64  `json:"dayStart"`
	DayEnd   int64  `json:"dayEnd"`
	// TZOffset 为路由器时区相对 UTC 的秒数，浏览器按此显示时间，不依赖访问设备的时区。
	TZOffset int `json:"tzOffset"`
	// IntervalSeconds 为一个采集周期的秒数，Periods × IntervalSeconds = 活跃秒数。
	IntervalSeconds int `json:"intervalSeconds"`
	BucketSeconds   int `json:"bucketSeconds"`
	Buckets         int `json:"buckets"`
	// LastCollectedAt 为最新一条明细的采集时刻，0 表示今天还没有数据。
	LastCollectedAt       int64 `json:"lastCollectedAt"`
	RecentSeconds         int   `json:"recentSeconds"`
	MinFlowBytesPerMinute int64 `json:"minFlowBytesPerMinute"`

	Categories []CategoryInfo `json:"categories"`
	Apps       []AppInfo      `json:"apps"`
	Devices    []DeviceInfo   `json:"devices"`

	// DeviceBuckets: [设备, 时段, 活跃周期数, 上行字节, 下行字节]
	DeviceBuckets [][5]int64 `json:"deviceBuckets"`
	// CategoryBuckets: [设备, 时段, 类别, 活跃周期数]
	CategoryBuckets [][4]int64 `json:"categoryBuckets"`
	// EntBuckets: [设备, 时段, 娱乐类别（任一）活跃周期数]
	EntBuckets [][3]int64 `json:"entBuckets"`
	// AppBuckets: [设备, 时段, 应用, 活跃周期数, 上行字节, 下行字节]
	AppBuckets [][6]int64 `json:"appBuckets"`
	// Recent: [设备, 应用, 活跃周期数]，统计最近 RecentSeconds 秒，用于"正在进行"。
	Recent [][3]int64 `json:"recent"`

	Stats Stats `json:"stats"`
}

// CategoryInfo 是一个行为类别。
type CategoryInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Entertainment bool   `json:"ent,omitempty"`
}

// AppInfo 是一个应用；Rule 为 false 表示规则库未收录、按主域名归并的网站，或"未识别流量"。
type AppInfo struct {
	Name     string `json:"name"`
	Category int    `json:"cat"`
	Rule     bool   `json:"rule,omitempty"`
}

// DeviceInfo 是今天出现过流量或当前在线的设备。
type DeviceInfo struct {
	ID       string   `json:"id"`
	MAC      string   `json:"mac,omitempty"`
	IP       string   `json:"ip"`
	Name     string   `json:"name,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	Hints    []string `json:"hints,omitempty"`
	// Online、Flows 来自 conntrack 实时状态（10 秒刷新）。
	Online       bool  `json:"online"`
	Flows        int   `json:"flows"`
	FirstSeen    int64 `json:"firstSeen,omitempty"`
	LastSeen     int64 `json:"lastSeen,omitempty"`
	LastActiveAt int64 `json:"lastActiveAt,omitempty"`
}

// Stats 记录本次聚合的规模，便于排查性能。
type Stats struct {
	Rows         int   `json:"rows"`
	InferredRows int   `json:"inferredRows"`
	UnknownRows  int   `json:"unknownRows"`
	BuildMillis  int64 `json:"buildMillis"`
}
