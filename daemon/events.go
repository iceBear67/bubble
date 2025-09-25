package daemon

type ServerEvent struct {
	typeIndex string
	flag      int
	data      interface{}
}

func CreateEventRaw(type_ string, flag int, data interface{}) *ServerEvent {
	return &ServerEvent{
		typeIndex: type_,
		flag:      flag,
		data:      data,
	}
}

func (c *ServerEvent) Flag() int {
	return c.flag
}

func (c *ServerEvent) DataRaw() any {
	return c.data
}

func (c *ServerEvent) Type() string {
	return c.typeIndex
}
