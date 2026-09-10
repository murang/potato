package nicepb

import (
	"example/nicepb/nice"
	"testing"

	"github.com/murang/potato/pb/vt"
	"google.golang.org/protobuf/proto"
)

var (
	testData []byte
)

func init() {
	// 准备测试数据
	msg := &nice.C2S_Complex{PastedObject: &nice.PastedObject{
		Company: "TechCorp Inc.",
		Founded: 2025,
		Public:  true,
		StockInfo: &nice.StockInfo{
			Symbol:    "Nice",
			Price:     123,
			Currency:  "CNY",
			MarketCap: "2.3T",
			Exchange:  "A",
			HistoricalData: []*nice.HistoricalData{
				&nice.HistoricalData{
					Date:  "2023-01-15",
					Open:  123,
					High:  321,
					Low:   123,
					Close: 321,
				},
				&nice.HistoricalData{
					Date:  "2023-01-15",
					Open:  123,
					High:  321,
					Low:   123,
					Close: 321,
				},
				&nice.HistoricalData{
					Date:  "2023-01-15",
					Open:  123,
					High:  321,
					Low:   123,
					Close: 321,
				},
			},
		},
		Departments: []*nice.Departments{
			{
				Name: "Engineering",
				Head: &nice.Head{
					FirstName:   "Johnson",
					LastName:    "Alice",
					EmployeeId:  "EMP001",
					Age:         12,
					Specialties: []string{"Go", "Python", "Java"},
				},
				Teams: []*nice.Teams{
					{
						TeamName:  "Backend Services",
						Members:   666,
						TechStack: []string{"Go", "Python", "Java"},
						Projects: []*nice.Projects{
							{
								Name:   "Payment Gateway",
								Status: "No Way",
								Budget: 5,
								Timeline: &nice.Timeline{
									Start: "2023-01-15",
									End:   "2023-01-15",
								},
							},
						},
					},
					{
						TeamName:  "Backend Services",
						Members:   666,
						TechStack: []string{"Go", "Python", "Java"},
						Projects: []*nice.Projects{
							{
								Name:   "Payment Gateway",
								Status: "No Way",
								Budget: 5,
								Timeline: &nice.Timeline{
									Start: "2023-01-15",
									End:   "2023-01-15",
								},
							},
						},
					},
					{
						TeamName:  "Backend Services",
						Members:   666,
						TechStack: []string{"Go", "Python", "Java"},
						Projects: []*nice.Projects{
							{
								Name:   "Payment Gateway",
								Status: "No Way",
								Budget: 5,
								Timeline: &nice.Timeline{
									Start: "2023-01-15",
									End:   "2023-01-15",
								},
							},
						},
					},
				},
			},
		},
		Locations: []*nice.Locations{
			{
				City:         "Shanghai",
				Country:      "China",
				Employees:    999,
				Headquarters: false,
				Facilities: &nice.Facilities{
					Offices:   12,
					Labs:      7,
					Amenities: []string{"Coffee", "Snacks", "Gym"},
				},
			},
		},
		Financials: &nice.Financials{
			FiscalYear: 4256,
			Revenue:    123,
			Expenses:   345,
			Profit:     345,
			Quarters: []*nice.Quarters{
				{
					Quarter: "ervervrv",
					Revenue: 1345,
					Profit:  345,
				},
				{
					Quarter: "ervervrv",
					Revenue: 1345,
					Profit:  345,
				},
				{
					Quarter: "ervervrv",
					Revenue: 1345,
					Profit:  345,
				},
			},
		},
		Metadata: &nice.Metadata{
			Version:     "fverver",
			Created:     "w	efwefwef",
			LastUpdated: "gqergqerg",
			Source:      "grtrtbwrt",
		},
	}}
	testData, _ = msg.MarshalVT()
}

// 直接调用 UnmarshalVT
func BenchmarkDirectVT(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var u nice.C2S_Complex
		_ = u.UnmarshalVT(testData)
	}
}

// 类型断言调用
func BenchmarkTypeAssertVT(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var u nice.C2S_Complex
		var m proto.Message = &u
		if v, ok := m.(interface{ UnmarshalVT([]byte) error }); ok {
			_ = v.UnmarshalVT(testData)
		}
	}
}

// 类型断言 + 注册表调用
func BenchmarkRegistryVT(b *testing.B) {
	vt.Register(&nice.C2S_Complex{}) // 确保注册一次

	for i := 0; i < b.N; i++ {
		var u nice.C2S_Complex
		_ = vt.Unmarshal(testData, &u)
	}
}

// 标准库 proto.Unmarshal
func BenchmarkProtoUnmarshal(b *testing.B) {
	for i := 0; i < b.N; i++ {
		var u nice.C2S_Complex
		_ = proto.Unmarshal(testData, &u)
	}
}

/*
goos: darwin
goarch: arm64
pkg: example/nicepb
cpu: Apple M4
BenchmarkDirectVT-10              829712              1389 ns/op
BenchmarkTypeAssertVT-10          818529              1404 ns/op
BenchmarkRegistryVT-10            850759              1412 ns/op
BenchmarkProtoUnmarshal-10        527241              2240 ns/op
*/
