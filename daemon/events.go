package daemon

type ServerEvent struct {
	typeIndex int
	flag      int
	data      interface{}
}

func CreateEventRaw(type_ int, flag int, data interface{}) *ServerEvent {
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

func (c *ServerEvent) Type() int {
	return c.typeIndex
}
