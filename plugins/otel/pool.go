package otel

import (
	"sync"
)

type ObjectPool struct {
	maxSize int

	resourceSpanAllocated int
	resourceSpanPool      sync.Pool

	scopeSpanAllocated int
	scopeSpanPool      sync.Pool

	spanAllocated int
	spanPool      sync.Pool

	eventAllocated int
	eventPool      sync.Pool

	keyValueAllocated int
	keyValuePool      sync.Pool

	anyValueAllocated int
	anyValuePool      sync.Pool

	arrayValueAllocated int
	arrayValuePool      sync.Pool

	listValueAllocated int
	listValuePool      sync.Pool

	stringValueAllocated int
	stringValuePool      sync.Pool

	intValueAllocated int
	intValuePool      sync.Pool

	doubleValueAllocated int
	doubleValuePool      sync.Pool

	boolValueAllocated int
	boolValuePool      sync.Pool
}

// NewObjectPool creates a new ObjectPool
// For max size, we are deliberately not using locks - as a bit of +- works
func NewObjectPool(maxSize int) *ObjectPool {
	return &ObjectPool{
		maxSize: maxSize,
		resourceSpanPool: sync.Pool{
			New: func() interface{} {
				return &ResourceSpan{}
			},
		},
		scopeSpanPool: sync.Pool{
			New: func() interface{} {
				return &ScopeSpan{}
			},
		},
		spanPool: sync.Pool{
			New: func() interface{} {
				return &Span{}
			},
		},
		eventPool: sync.Pool{
			New: func() interface{} {
				return &Event{}
			},
		},
		keyValuePool: sync.Pool{
			New: func() interface{} {
				return &KeyValue{}
			},
		},
		anyValuePool: sync.Pool{
			New: func() interface{} {
				return &AnyValue{}
			},
		},
		arrayValuePool: sync.Pool{
			New: func() interface{} {
				return &ArrayValue{}
			},
		},
		listValuePool: sync.Pool{
			New: func() interface{} {
				return &ListValue{}
			},
		},
		stringValuePool: sync.Pool{
			New: func() interface{} {
				return &StringValue{}
			},
		},
		intValuePool: sync.Pool{
			New: func() interface{} {
				return &IntValue{}
			},
		},
		doubleValuePool: sync.Pool{
			New: func() interface{} {
				return &DoubleValue{}
			},
		},
	}
}

// getResourceSpan gets a ResourceSpan from the pool
func (p *ObjectPool) getResourceSpan() *ResourceSpan {
	if p.resourceSpanPool.Get() == nil {
		p.resourceSpanAllocated++
		return &ResourceSpan{}
	}
	return p.resourceSpanPool.Get().(*ResourceSpan)
}

// putResourceSpan returns a ResourceSpan to the pool
func (p *ObjectPool) putResourceSpan(span *ResourceSpan) {
	if p.resourceSpanAllocated <= p.maxSize {
		p.resourceSpanPool.Put(span)
		return
	}
	p.resourceSpanAllocated--
}

// getScopeSpan gets a ScopeSpan from the pool
func (p *ObjectPool) getScopeSpan() *ScopeSpan {
	if p.scopeSpanPool.Get() == nil {
		p.scopeSpanAllocated++
		return &ScopeSpan{}
	}
	return p.scopeSpanPool.Get().(*ScopeSpan)
}

// putScopeSpan returns a ScopeSpan to the pool
func (p *ObjectPool) putScopeSpan(span *ScopeSpan) {
	if p.scopeSpanAllocated <= p.maxSize {
		p.scopeSpanPool.Put(span)
		return
	}
	p.scopeSpanAllocated--
}

// getSpan gets a Span from the pool
func (p *ObjectPool) getSpan() *Span {
	if p.spanPool.Get() == nil {
		p.spanAllocated++
		return &Span{}
	}
	return p.spanPool.Get().(*Span)
}

// putSpan returns a Span to the pool
func (p *ObjectPool) putSpan(span *Span) {
	if p.spanAllocated <= p.maxSize {
		p.spanPool.Put(span)
		return
	}
	p.spanAllocated--
}

// getEvent gets an Event from the pool
func (p *ObjectPool) getEvent() *Event {
	if p.eventPool.Get() == nil {
		p.eventAllocated++
		return &Event{}
	}
	return p.eventPool.Get().(*Event)
}

// putEvent returns an Event to the pool
func (p *ObjectPool) putEvent(event *Event) {
	if p.eventAllocated <= p.maxSize {
		p.eventPool.Put(event)
		return
	}
	p.eventAllocated--
}

// getKeyValue gets a KeyValue from the pool
func (p *ObjectPool) getKeyValue() *KeyValue {
	if p.keyValuePool.Get() == nil {
		p.keyValueAllocated++
		return &KeyValue{}
	}
	return p.keyValuePool.Get().(*KeyValue)
}

// putKeyValue returns a KeyValue to the pool
func (p *ObjectPool) putKeyValue(keyValue *KeyValue) {
	if p.keyValueAllocated <= p.maxSize {
		p.keyValuePool.Put(keyValue)
		return
	}
	p.keyValueAllocated--
}

// getAnyValue gets an AnyValue from the pool
func (p *ObjectPool) getAnyValue() *AnyValue {
	if p.anyValuePool.Get() == nil {
		p.anyValueAllocated++
		return &AnyValue{}
	}
	return p.anyValuePool.Get().(*AnyValue)
}

// putAnyValue returns an AnyValue to the pool
func (p *ObjectPool) putAnyValue(anyValue *AnyValue) {
	if p.anyValueAllocated <= p.maxSize {
		p.anyValuePool.Put(anyValue)
		return
	}
	p.anyValueAllocated--
}

// getArrayValue gets an ArrayValue from the pool
func (p *ObjectPool) getArrayValue() *ArrayValue {
	if p.arrayValuePool.Get() == nil {
		p.arrayValueAllocated++
		return &ArrayValue{}
	}
	return p.arrayValuePool.Get().(*ArrayValue)
}

// putArrayValue returns an ArrayValue to the pool
func (p *ObjectPool) putArrayValue(arrayValue *ArrayValue) {
	if p.arrayValueAllocated <= p.maxSize {
		p.arrayValuePool.Put(arrayValue)
		return
	}
	p.arrayValueAllocated--
}

// getListValue gets a ListValue from the pool
func (p *ObjectPool) getListValue() *ListValue {
	if p.listValuePool.Get() == nil {
		p.listValueAllocated++
		return &ListValue{}
	}
	return p.listValuePool.Get().(*ListValue)
}

// putListValue returns a ListValue to the pool
func (p *ObjectPool) putListValue(listValue *ListValue) {
	if p.listValueAllocated <= p.maxSize {
		p.listValuePool.Put(listValue)
		return
	}
	p.listValueAllocated--
}

// getStringValue gets a StringValue from the pool
func (p *ObjectPool) getStringValue(value string) *StringValue {
	if p.stringValuePool.Get() == nil {
		p.stringValueAllocated++
		return &StringValue{StringValue: value}
	}
	strValue := p.stringValuePool.Get().(*StringValue)
	strValue.StringValue = value
	return strValue
}

// putStringValue returns a StringValue to the pool
func (p *ObjectPool) putStringValue(stringValue *StringValue) {
	if p.stringValueAllocated <= p.maxSize {
		p.stringValuePool.Put(stringValue)
		return
	}
	p.stringValueAllocated--
}

// getIntValue gets an IntValue from the pool
func (p *ObjectPool) getIntValue() *IntValue {
	if p.intValuePool.Get() == nil {
		p.intValueAllocated++
		return &IntValue{}
	}
	return p.intValuePool.Get().(*IntValue)
}

// putIntValue returns an IntValue to the pool
func (p *ObjectPool) putIntValue(intValue *IntValue) {
	if p.intValueAllocated <= p.maxSize {
		p.intValuePool.Put(intValue)
		return
	}
	p.intValueAllocated--
}

// getDoubleValue gets a DoubleValue from the pool
func (p *ObjectPool) getDoubleValue() *DoubleValue {
	if p.doubleValuePool.Get() == nil {
		p.doubleValueAllocated++
		return &DoubleValue{}
	}
	return p.doubleValuePool.Get().(*DoubleValue)
}

// putDoubleValue returns a DoubleValue to the pool
func (p *ObjectPool) putDoubleValue(doubleValue *DoubleValue) {
	if p.doubleValueAllocated <= p.maxSize {
		p.doubleValuePool.Put(doubleValue)
		return
	}
	p.doubleValueAllocated--
}

// getBoolValue gets a BoolValue from the pool
func (p *ObjectPool) getBoolValue() *BoolValue {
	if p.boolValuePool.Get() == nil {
		p.boolValueAllocated++
		return &BoolValue{}
	}
	return p.boolValuePool.Get().(*BoolValue)
}

// putBoolValue returns a BoolValue to the pool
func (p *ObjectPool) putBoolValue(boolValue *BoolValue) {
	if p.boolValueAllocated <= p.maxSize {
		p.boolValuePool.Put(boolValue)
		return
	}
	p.boolValueAllocated--
}
