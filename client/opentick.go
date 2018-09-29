package client

import (
	"encoding/binary"
	"errors"
	"gopkg.in/mgo.v2/bson"
	"net"
	"strconv"
)

type Future interface {
	Get() ([][]interface{}, error)
}

// not thread-safe
type Connection interface {
	Execute(sql string, args ...interface{}) (ret [][]interface{}, err error)
	ExecuteAsync(sql string, args ...interface{}) (Future, error)
	Close()
}

func Connect(host string, port int, dbName string) (ret Connection, err error) {
	conn, err := net.Dial("tcp", host+":"+strconv.FormatInt(int64(port), 10))
	if err != nil {
		return
	}
	c := connection{conn: conn, prepared: make(map[string]int), store: make(map[int]interface{}), ch: make(chan interface{})}
	go recv(c)
	token := c.tokenCounter
	c.tokenCounter++
	if dbName != "" {
		cmd := map[string]interface{}{"0": token, "1": "use", "2": dbName}
		err = c.send(cmd)
		if err != nil {
			c.Close()
			return
		}
		f := future{token, &c}
		_, err = f.get()
		if err != nil {
			c.Close()
			return
		}
	}
	ret = &c
	return
}

type future struct {
	token int
	conn  *connection
}

func (self *future) get() (interface{}, error) {
	var res interface{}
	if tmp, ok := self.conn.store[self.token]; ok {
		delete(self.conn.store, self.token)
		res = tmp
	} else {
		for {
			select {
			case data, ok := <-self.conn.ch:
				if !ok {
					return nil, nil
				}
				if err, _ := data.(error); err != nil {
					return nil, err
				}
				tmp := data.(map[string]interface{})
				token := tmp["0"].(int)
				res = tmp["1"]
				if token == self.token {
					goto done
				} else {
					self.conn.store[token] = res
				}
			}
		}
	}
done:
	if str, ok := res.(string); ok {
		return nil, errors.New(str)
	}
	return res, nil
}

func (self *future) Get() ([][]interface{}, error) {
	res, err := self.get()
	if res == nil || err != nil {
		return nil, err
	}
	ret, _ := res.([][]interface{})
	return ret, nil
}

type connection struct {
	conn         net.Conn
	tokenCounter int
	prepared     map[string]int
	store        map[int]interface{}
	ch           chan interface{}
}

func (self *connection) Close() {
	self.conn.Close()
}

func (self *connection) Execute(sql string, args ...interface{}) (ret [][]interface{}, err error) {
	var fut Future
	fut, err = self.ExecuteAsync(sql, args...)
	if err != nil {
		return
	}
	return fut.Get()
}

func (self *connection) ExecuteAsync(sql string, args ...interface{}) (ret Future, err error) {
	prepared := -1
	var cmd map[string]interface{}
	if len(args) > 0 {
		var ok bool
		if prepared, ok = self.prepared[sql]; !ok {
			token := self.tokenCounter
			self.tokenCounter++
			cmd = map[string]interface{}{"0": token, "1": "prepare", "2": sql}
			err = self.send(cmd)
			if err != nil {
				return
			}
			f := future{token, self}
			res, err2 := f.get()
			if err2 != nil {
				err = err2
				return
			}
			prepared = res.(int)
			self.prepared[sql] = prepared
		}
	}
	token := self.tokenCounter
	self.tokenCounter++
	cmd = map[string]interface{}{"0": token, "1": "run", "2": sql, "3": args}
	if prepared >= 0 {
		cmd["2"] = prepared
	}
	err = self.send(cmd)
	if err != nil {
		return
	}
	ret = &future{token, self}
	return
}

func (self *connection) send(data map[string]interface{}) error {
	out, err := bson.Marshal(data)
	if err != nil {
		panic(err)
	}
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(out)))
	out = append(size[:], out...)
	n := len(out)
	for n > 0 {
		n2, err := self.conn.Write(out)
		if err != nil {
			return err
		}
		n -= n2
		out = out[2:]
	}
	return nil
}

func recv(c connection) {
	for {
		var head [4]byte
		tmp := head[:4]
		for n, err := c.conn.Read(tmp); n < len(tmp); {
			tmp = tmp[n:]
			if err != nil {
				if c.ch != nil {
					c.ch <- err
				}
				return
			}
		}
		n := binary.LittleEndian.Uint32(head[:])
		if n == 0 {
			continue
		}
		body := make([]byte, n)
		tmp = body
		for n, err := c.conn.Read(tmp); n < len(tmp); {
			tmp = tmp[n:]
			if err != nil {
				c.ch <- err
				return
			}
		}
		var data map[string]interface{}
		var err error
		err = bson.Unmarshal(body, &data)
		if err != nil {
			c.ch <- err
			return
		}
		c.ch <- data
	}
}