package ftl

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestMessageSplitting(t *testing.T) {
	assert := assert.New(t)

	_, err := Dial("localhost:8084", 1234, []byte("some-bullshit"))
	if err != nil {
		panic(err)
	}

	assert.Equal(1, 1)
}
