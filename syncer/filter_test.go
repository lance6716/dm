// Copyright 2019 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package syncer

import (
	"context"
	"database/sql"

	"github.com/DATA-DOG/go-sqlmock"
	. "github.com/pingcap/check"
	"github.com/pingcap/parser"
	bf "github.com/pingcap/tidb-tools/pkg/binlog-filter"
	"github.com/pingcap/tidb-tools/pkg/filter"
	"github.com/shopspring/decimal"

	"github.com/pingcap/dm/pkg/conn"
	"github.com/pingcap/dm/pkg/log"
	"github.com/pingcap/dm/pkg/schema"
)

type testFilterSuite struct {
	baseConn *conn.BaseConn
	db       *sql.DB
}

var _ = Suite(&testFilterSuite{})

func (s *testFilterSuite) SetUpSuite(c *C) {
	db, mock, err := sqlmock.New()
	c.Assert(err, IsNil)
	s.db = db
	mock.ExpectClose()
	con, err := db.Conn(context.Background())
	c.Assert(err, IsNil)
	s.baseConn = conn.NewBaseConn(con, nil)
}

func (s *testFilterSuite) TearDownSuite(c *C) {
	c.Assert(s.baseConn.DBConn.Close(), IsNil)
	c.Assert(s.db.Close(), IsNil)
}

func (s *testFilterSuite) TestSkipQueryEvent(c *C) {
	cases := []struct {
		sql           string
		expectSkipped bool
	}{
		{"SAVEPOINT `a1`", true},

		// flush
		{"flush privileges", true},
		{"flush logs", true},
		{"FLUSH TABLES WITH READ LOCK", true},

		// table maintenance
		{"OPTIMIZE TABLE foo", true},
		{"ANALYZE TABLE foo", true},
		{"REPAIR TABLE foo", true},

		// temporary table
		{"DROP /*!40005 TEMPORARY */ TABLE IF EXISTS `h2`", true},
		{"DROP TEMPORARY TABLE IF EXISTS `foo`.`bar` /* generated by server */", true},
		{"DROP TABLE foo.bar", false},
		{"DROP TABLE `TEMPORARY TABLE`", false},
		{"DROP TABLE `TEMPORARY TABLE` /* generated by server */", false},

		// trigger
		{"CREATE DEFINER=`root`@`%` TRIGGER ins_sum BEFORE INSERT ON bar FOR EACH ROW SET @sum = @sum + NEW.id", true},
		{"CREATE TRIGGER ins_sum BEFORE INSERT ON bar FOR EACH ROW SET @sum = @sum + NEW.id", true},
		{"DROP TRIGGER ins_sum", true},
		{"create table `trigger`(id int)", false},

		// procedure
		{"drop procedure if exists prepare_data", true},
		{"CREATE DEFINER=`root`@`%` PROCEDURE `simpleproc`(OUT param1 INT) BEGIN  select count(*) into param1 from shard_0001; END", true},
		{"CREATE PROCEDURE simpleproc(OUT param1 INT) BEGIN  select count(*) into param1 from shard_0001; END", true},
		{"alter procedure prepare_data comment 'i am a comment'", true},
		{"create table `procedure`(id int)", false},

		{`CREATE DEFINER=root@localhost PROCEDURE simpleproc(OUT param1 INT)
BEGIN
    SELECT COUNT(*) INTO param1 FROM t;
END`, true},

		// view
		{"CREATE ALGORITHM=UNDEFINED DEFINER=`root`@`%` SQL SECURITY DEFINER VIEW `v` AS SELECT qty, price, qty*price AS value FROM t", true},
		{"CREATE OR REPLACE ALGORITHM=UNDEFINED DEFINER=`root`@`%` SQL SECURITY DEFINER VIEW `v` AS SELECT qty, price, qty*price AS value FROM t", true},
		{"ALTER ALGORITHM=UNDEFINED DEFINER=`root`@`%` SQL SECURITY DEFINER VIEW `v` AS SELECT qty, price, qty*price AS value FROM t", true},
		{"DROP VIEW v", true},
		{"CREATE TABLE `VIEW`(id int)", false},
		{"ALTER TABLE `VIEW`(id int)", false},

		// function
		{"CREATE FUNCTION metaphon RETURNS STRING SONAME 'udf_example.so'", true},
		{"CREATE AGGREGATE FUNCTION avgcost RETURNS REAL SONAME 'udf_example.so'", true},
		{"DROP FUNCTION metaphon", true},
		{"DROP FUNCTION IF EXISTS `rand_string`", true},
		{"ALTER FUNCTION metaphon COMMENT 'hh'", true},
		{"CREATE TABLE `function` (id int)", false},

		{`CREATE DEFINER=root@localhost FUNCTION rand_string(n INT) RETURNS varchar(255) CHARSET utf8
BEGIN
          DECLARE chars_str VARCHAR(100) DEFAULT 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ';
          DECLARE return_str VARCHAR(255) DEFAULT '';
          DECLARE i INT DEFAULT 0;
          WHILE i<n DO
              SET return_str = CONCAT(return_str,SUBSTRING(chars_str,FLOOR(1+RAND()*52),1));
              SET i = i+1;
          END WHILE;
    RETURN return_str;
END`, true},

		// tablespace
		{"CREATE TABLESPACE `ts1` ADD DATAFILE 'ts1.ibd' ENGINE=INNODB", true},
		{"ALTER TABLESPACE `ts1` DROP DATAFILE 'ts1.idb' ENGIEN=NDB", true},
		{"DROP TABLESPACE ts1", true},

		// event
		{"CREATE DEFINER=CURRENT_USER EVENT myevent ON SCHEDULE AT CURRENT_TIMESTAMP + INTERVAL 1 HOUR DO UPDATE myschema.mytable SET mycol = mycol + 1;", true},
		{"ALTER DEFINER = CURRENT_USER EVENT myevent ON SCHEDULE EVERY 12 HOUR STARTS CURRENT_TIMESTAMP + INTERVAL 4 HOUR;", true},
		{"DROP EVENT myevent;", true},

		// account management
		{"CREATE USER 't'@'%' IDENTIFIED WITH 'mysql_native_password' AS '*93E34F4B81FEC9E8271655EA87646ED01AF377CC'", true},
		{"ALTER USER 't'@'%' IDENTIFIED WITH 'mysql_native_password' AS '*1114744159A0EF13B12FC371C94877763F9512D0'", true},
		{"rename user t to 1", true},
		{"drop user t1", true},
		{"GRANT ALL PRIVILEGES ON *.* TO 't2'@'%' IDENTIFIED WITH 'mysql_native_password' AS '*12033B78389744F3F39AC4CE4CCFCAD6960D8EA0'", true},
		{"revoke reload on *.* from 't2'@'%'", true},
	}

	// filter, err := bf.NewBinlogEvent(nil)
	// c.Assert(err, IsNil)
	syncer := &Syncer{}
	for _, t := range cases {
		skipped, err := syncer.skipQuery(nil, nil, t.sql)
		c.Assert(err, IsNil)
		c.Assert(skipped, Equals, t.expectSkipped)
	}

	// system table
	skipped, err := syncer.skipQuery([]*filter.Table{{Schema: "mysql", Name: "test"}}, nil, "create table mysql.test (id int)")
	c.Assert(err, IsNil)
	c.Assert(skipped, Equals, true)

	// test binlog filter
	filterRules := []*bf.BinlogEventRule{
		{
			SchemaPattern: "*",
			TablePattern:  "",
			Events:        []bf.EventType{bf.DropTable},
			SQLPattern:    []string{"^drop\\s+table"},
			Action:        bf.Ignore,
		}, {
			SchemaPattern: "foo*",
			TablePattern:  "",
			Events:        []bf.EventType{bf.CreateTable},
			SQLPattern:    []string{"^create\\s+table"},
			Action:        bf.Do,
		}, {
			SchemaPattern: "foo*",
			TablePattern:  "bar*",
			Events:        []bf.EventType{bf.CreateTable},
			SQLPattern:    []string{"^create\\s+table"},
			Action:        bf.Ignore,
		},
	}

	syncer.binlogFilter, err = bf.NewBinlogEvent(false, filterRules)
	c.Assert(err, IsNil)

	// test global rule
	sql := "drop table tx.test"
	stmt, err := parser.New().ParseOneStmt(sql, "", "")
	c.Assert(err, IsNil)
	skipped, err = syncer.skipQuery([]*filter.Table{{Schema: "tx", Name: "test"}}, stmt, sql)
	c.Assert(err, IsNil)
	c.Assert(skipped, Equals, true)

	sql = "create table tx.test (id int)"
	stmt, err = parser.New().ParseOneStmt(sql, "", "")
	c.Assert(err, IsNil)
	skipped, err = syncer.skipQuery([]*filter.Table{{Schema: "tx", Name: "test"}}, stmt, sql)
	c.Assert(err, IsNil)
	c.Assert(skipped, Equals, false)

	// test schema rule
	sql = "create table foo.test(id int)"
	stmt, err = parser.New().ParseOneStmt(sql, "", "")
	c.Assert(err, IsNil)
	skipped, err = syncer.skipQuery([]*filter.Table{{Schema: "foo", Name: "test"}}, stmt, sql)
	c.Assert(err, IsNil)
	c.Assert(skipped, Equals, false)

	sql = "rename table foo.test to foo.test1"
	stmt, err = parser.New().ParseOneStmt(sql, "", "")
	c.Assert(err, IsNil)
	skipped, err = syncer.skipQuery([]*filter.Table{{Schema: "foo", Name: "test"}}, stmt, sql)
	c.Assert(err, IsNil)
	c.Assert(skipped, Equals, true)

	// test table rule
	sql = "create table foo.bar(id int)"
	stmt, err = parser.New().ParseOneStmt(sql, "", "")
	c.Assert(err, IsNil)
	skipped, err = syncer.skipQuery([]*filter.Table{{Schema: "foo", Name: "bar"}}, stmt, sql)
	c.Assert(err, IsNil)
	c.Assert(skipped, Equals, true)
}

func (s *testFilterSuite) TestSkipDMLByExpression(c *C) {
	cases := []struct {
		exprStr    string
		tableStr   string
		skippedRow []interface{}
		passedRow  []interface{}
	}{
		{
			"state != 1",
			`
create table t (
	primary_id bigint(20) unsigned NOT NULL AUTO_INCREMENT,
	id bigint(20) unsigned NOT NULL,
	state tinyint(3) unsigned NOT NULL,
	PRIMARY KEY (primary_id),
	UNIQUE KEY uniq_id (id),
	KEY idx_state (state)
);`,
			[]interface{}{100, 100, 3},
			[]interface{}{100, 100, 1},
		},
		{
			"f > 1.23",
			`
create table t (
	f float
);`,
			[]interface{}{float32(2.0)},
			[]interface{}{float32(1.0)},
		},
		{
			"f > a + b",
			`
create table t (
	f float,
	a int,
	b int
);`,
			[]interface{}{float32(123.45), 1, 2},
			[]interface{}{float32(0.01), 23, 45},
		},
		{
			"id = 30",
			`
create table t (
	id int(11) NOT NULL AUTO_INCREMENT,
	name varchar(20) COLLATE utf8mb4_bin DEFAULT NULL,
	dt datetime DEFAULT NULL,
	ts timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
	PRIMARY KEY (id)
);`,
			[]interface{}{30, "30", nil, "2021-06-17 10:13:05"},
			[]interface{}{20, "20", nil, "2021-06-17 10:13:05"},
		},
	}

	var (
		ctx = context.Background()
		db  = "test"
		tbl = "t"
	)
	c.Assert(log.InitLogger(&log.Config{Level: "debug"}), IsNil)

	for _, ca := range cases {
		var (
			err    error
			syncer = &Syncer{}
		)
		syncer.schemaTracker, err = schema.NewTracker(ctx, "unit-test", defaultTestSessionCfg, s.baseConn)
		c.Assert(err, IsNil)
		c.Assert(syncer.schemaTracker.CreateSchemaIfNotExists(db), IsNil)
		c.Assert(syncer.schemaTracker.Exec(ctx, db, ca.tableStr), IsNil)
		expr, err := syncer.schemaTracker.GetSimpleExprOfTable(db, tbl, ca.exprStr)
		c.Assert(err, IsNil)

		skip, err := SkipDMLByExpression(ca.skippedRow, expr)
		c.Assert(err, IsNil)
		c.Assert(skip, Equals, true)

		skip, err = SkipDMLByExpression(ca.passedRow, expr)
		c.Assert(err, IsNil)
		c.Assert(skip, Equals, false)

		c.Assert(syncer.schemaTracker.Close(), IsNil)
	}
}

func (s *testFilterSuite) TestAllBinaryProtocolTypes(c *C) {
	skippedDec, err := decimal.NewFromString("10.10")
	c.Assert(err, IsNil)
	passedDec, err := decimal.NewFromString("10.11")

	// https://github.com/go-mysql-org/go-mysql/blob/a18ba90219c438df600fd3e4a64edb7e344c75aa/replication/row_event.go#L994
	c.Assert(err, IsNil)
	cases := []struct {
		exprStr    string
		tableStr   string
		skippedRow []interface{}
		passedRow  []interface{}
	}{
		// MYSQL_TYPE_NULL
		{
			"c IS NULL",
			`
create table t (
	c int
);`,
			[]interface{}{nil},
			[]interface{}{100},
		},
		// MYSQL_TYPE_LONG
		{
			"c = 1",
			`
create table t (
	c int
);`,
			[]interface{}{int32(1)},
			[]interface{}{int32(100)},
		},
		// MYSQL_TYPE_TINY
		{
			"c = 2",
			`
create table t (
	c tinyint
);`,
			[]interface{}{int8(2)},
			[]interface{}{int8(-1)},
		},
		// MYSQL_TYPE_SHORT
		{
			"c < 10",
			`
create table t (
	c smallint
);`,
			[]interface{}{int16(8)},
			[]interface{}{int16(18)},
		},
		// MYSQL_TYPE_INT24
		{
			"c < 0",
			`
create table t (
	c mediumint
);`,
			[]interface{}{int32(-8)},
			[]interface{}{int32(1)},
		},
		// MYSQL_TYPE_LONGLONG
		{
			"c = 100000000",
			`
create table t (
	c bigint
);`,
			[]interface{}{int64(100000000)},
			[]interface{}{int64(200000000)},
		},
		// MYSQL_TYPE_NEWDECIMAL
		// DM always set UseDecimal to true
		{
			"c = 10.1",
			`
create table t (
	c decimal(5,2)
);`,
			[]interface{}{skippedDec},
			[]interface{}{passedDec},
		},
		// MYSQL_TYPE_FLOAT
		{
			"c < 0.1",
			`
create table t (
	c float
);`,
			[]interface{}{float32(0.08)},
			[]interface{}{float32(0.18)},
		},
		// MYSQL_TYPE_DOUBLE
		{
			"c < 0.1",
			`
create table t (
	c double
);`,
			[]interface{}{float64(0.08)},
			[]interface{}{float64(0.18)},
		},
		// MYSQL_TYPE_BIT
		{
			"c = b'1'",
			`
create table t (
	c bit(4)
);`,
			[]interface{}{int64(1)},
			[]interface{}{int64(2)},
		},
		// MYSQL_TYPE_TIMESTAMP, MYSQL_TYPE_TIMESTAMP2
		// DM does not set ParseTime
		// TODO: use upstream timezone later
		{
			"c = '2021-06-21 12:34:56'",
			`
create table t (
	c timestamp
);`,
			[]interface{}{"2021-06-21 12:34:56"},
			[]interface{}{"1970-02-01 00:00:01"},
		},
		// MYSQL_TYPE_DATETIME, MYSQL_TYPE_DATETIME2
		{
			"c = '2021-06-21 00:00:12'",
			`
create table t (
	c datetime
);`,
			[]interface{}{"2021-06-21 00:00:12"},
			[]interface{}{"1970-01-01 00:00:01"},
		},
		// MYSQL_TYPE_TIME, MYSQL_TYPE_TIME2
		{
			"c = '00:00:12'",
			`
create table t (
	c time(6)
);`,
			[]interface{}{"00:00:12"},
			[]interface{}{"00:00:01"},
		},
		// MYSQL_TYPE_DATE
		{
			"c = '2021-06-21'",
			`
create table t (
	c date
);`,
			[]interface{}{"2021-06-21"},
			[]interface{}{"1970-01-01"},
		},
		// MYSQL_TYPE_YEAR
		{
			"c = '2021'",
			`
create table t (
	c year
);`,
			[]interface{}{int(2021)},
			[]interface{}{int(2020)},
		},
		// MYSQL_TYPE_ENUM
		{
			"c = 'x-small'",
			`
create table t (
	c ENUM('x-small', 'small', 'medium', 'large', 'x-large')
);`,
			[]interface{}{int64(1)},
			[]interface{}{int64(2)},
		},
		// MYSQL_TYPE_SET
		{
			"find_in_set('c', c) > 0",
			`
create table t (
	c SET('a', 'b', 'c', 'd')
);`,
			[]interface{}{int64(0b1100)},
			[]interface{}{int64(0b1000)},
		},
		// MYSQL_TYPE_BLOB
		{
			"c = x'1234'",
			`
create table t (
	c blob
);`,
			[]interface{}{[]byte("\x124")},
			[]interface{}{[]byte("Vx")},
		},
		// MYSQL_TYPE_VARCHAR, MYSQL_TYPE_VAR_STRING, MYSQL_TYPE_STRING
		{
			"c = 'abc'",
			`
create table t (
	c varchar(20)
);`,
			[]interface{}{"abc"},
			[]interface{}{"def"},
		},
		// MYSQL_TYPE_JSON
		{
			`c->"$.id" = 1`,
			`
create table t (
	c json
);`,
			[]interface{}{[]byte(`{"id": 1}`)},
			[]interface{}{[]byte(`{"id": 2}`)},
		},
		// MYSQL_TYPE_GEOMETRY, parser not supported
	}

	var (
		ctx = context.Background()
		db  = "test"
		tbl = "t"
	)
	c.Assert(log.InitLogger(&log.Config{Level: "debug"}), IsNil)

	for _, ca := range cases {
		c.Log(ca.tableStr)
		var (
			err    error
			syncer = &Syncer{}
		)
		syncer.schemaTracker, err = schema.NewTracker(ctx, "unit-test", defaultTestSessionCfg, s.baseConn)
		c.Assert(err, IsNil)
		c.Assert(syncer.schemaTracker.CreateSchemaIfNotExists(db), IsNil)
		c.Assert(syncer.schemaTracker.Exec(ctx, db, ca.tableStr), IsNil)
		expr, err := syncer.schemaTracker.GetSimpleExprOfTable(db, tbl, ca.exprStr)
		c.Assert(err, IsNil)

		skip, err := SkipDMLByExpression(ca.skippedRow, expr)
		c.Assert(err, IsNil)
		c.Assert(skip, Equals, true)

		skip, err = SkipDMLByExpression(ca.passedRow, expr)
		c.Assert(err, IsNil)
		c.Assert(skip, Equals, false)

		c.Assert(syncer.schemaTracker.Close(), IsNil)
	}
}

func (s *testFilterSuite) TestExpressionContainsNonExistColumn(c *C) {
	var (
		err      error
		syncer   = &Syncer{}
		ctx      = context.Background()
		db       = "test"
		tbl      = "t"
		tableStr = `
create table t (
	c varchar(20)
);`
		exprStr = "d > 1"
	)
	syncer.schemaTracker, err = schema.NewTracker(ctx, "unit-test", defaultTestSessionCfg, s.baseConn)
	c.Assert(err, IsNil)
	c.Assert(syncer.schemaTracker.CreateSchemaIfNotExists(db), IsNil)
	c.Assert(syncer.schemaTracker.Exec(ctx, db, tableStr), IsNil)
	expr, err := syncer.schemaTracker.GetSimpleExprOfTable(db, tbl, exprStr)
	c.Assert(err, IsNil)
	c.Assert(expr.Expression.String(), Equals, "0")

	// skip nothing
	skip, err := SkipDMLByExpression([]interface{}{0}, expr)
	c.Assert(err, IsNil)
	c.Assert(skip, Equals, false)

	skip, err = SkipDMLByExpression([]interface{}{2}, expr)
	c.Assert(err, IsNil)
	c.Assert(skip, Equals, false)
}
