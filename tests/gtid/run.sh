#!/bin/bash

set -eu

cur=$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )
source $cur/../_utils/test_prepare
WORK_DIR=$TEST_DIR/$TEST_NAME
API_VERSION="v1alpha1"
TASK_NAME="test"

function run() {
    run_sql_file $cur/data/db1.prepare.sql $MYSQL_HOST1 $MYSQL_PORT1 $MYSQL_PASSWORD1
    run_sql_file $cur/data/db2.prepare.sql $MYSQL_HOST2 $MYSQL_PORT2 $MYSQL_PASSWORD2

    cp $cur/conf/source1.yaml $WORK_DIR/source1.yaml
    cp $cur/conf/source2.yaml $WORK_DIR/source2.yaml
    sed -i "/from:/i\relay-dir: $WORK_DIR/worker1/relay_log" $WORK_DIR/source1.yaml
    sed -i "/from:/i\relay-dir: $WORK_DIR/worker2/relay_log" $WORK_DIR/source2.yaml

    # start DM worker and source one-by-one, make sure the source1 bound to worker1
    run_dm_master $WORK_DIR/master $MASTER_PORT $cur/conf/dm-master.toml
    check_rpc_alive $cur/../bin/check_master_online 127.0.0.1:$MASTER_PORT
    run_dm_worker $WORK_DIR/worker1 $WORKER1_PORT $cur/conf/dm-worker1.toml
    check_rpc_alive $cur/../bin/check_worker_online 127.0.0.1:$WORKER1_PORT
    dmctl_operate_source create $WORK_DIR/source1.yaml $SOURCE_ID1

    run_dm_worker $WORK_DIR/worker2 $WORKER2_PORT $cur/conf/dm-worker2.toml
    check_rpc_alive $cur/../bin/check_worker_online 127.0.0.1:$WORKER2_PORT
    dmctl_operate_source create $WORK_DIR/source2.yaml $SOURCE_ID2

    dmctl_start_task "$cur/conf/dm-task.yaml" "--remove-meta"
    check_sync_diff $WORK_DIR $cur/conf/diff_config.toml

    # TODO: when there's a purged gap, if starting gtid set covers gap, there should be no data lost
    # if starting gtid set not fully covers gap, behaviour should be same whether we enable relay
    # hard to reproduce in CI

    # when there's a not purged gap, there should be no data lost
    # we manually `set gtid_next = 'uuid:gtid'`to reproduce
    gtid1=$(grep "GTID:" $WORK_DIR/worker1/dumped_data.$TASK_NAME/metadata|awk -F: '{print $2,":",$3}'|tr -d ' ')
    gtid2=$(grep "GTID:" $WORK_DIR/worker2/dumped_data.$TASK_NAME/metadata|awk -F: '{print $2,":",$3}'|tr -d ' ')
    uuid1=$(echo $gtid1 | awk -F: '{print $1}')
    uuid2=$(echo $gtid2 | awk -F: '{print $1}')
    end_gtid_num1=$(echo $gtid1 | awk -F: '{print $2}' | awk -F- '{print $2}')
    end_gtid_num2=$(echo $gtid2 | awk -F: '{print $2}' | awk -F- '{print $2}')
    new_gtid1=${uuid1}:$((end_gtid_num1 + 3))
    new_gtid2=${uuid2}:$((end_gtid_num2 + 3))
    echo "new_gtid1 $new_gtid1 new_gtid2 $new_gtid2"

    run_sql "SET gtid_next='$new_gtid1';insert into gtid.t1 values (3);SET gtid_next='AUTOMATIC';" $MYSQL_PORT1 $MYSQL_PASSWORD1
    run_sql "SET gtid_next='$new_gtid2';insert into gtid.t2 values (3);SET gtid_next='AUTOMATIC'" $MYSQL_PORT2 $MYSQL_PASSWORD2
    run_sql "flush logs" $MYSQL_PORT1 $MYSQL_PASSWORD1
    run_sql "flush logs" $MYSQL_PORT2 $MYSQL_PASSWORD2
    run_sql "insert into gtid.t1 values (4)" $MYSQL_PORT1 $MYSQL_PASSWORD1
    run_sql "insert into gtid.t2 values (4)" $MYSQL_PORT2 $MYSQL_PASSWORD2
    # now Previous_gtids event is 09bec856-ba95-11ea-850a-58f2b4af5188:1-4:6

    sleep 1
    run_dm_ctl $WORK_DIR "127.0.0.1:$MASTER_PORT" \
        "stop-task test"\
        "\"result\": true" 3

    run_sql "insert into gtid.t1 values (5)" $MYSQL_PORT1 $MYSQL_PASSWORD1
    run_sql "insert into gtid.t2 values (5)" $MYSQL_PORT2 $MYSQL_PASSWORD2
    # now Previous_gtids event is 09bec856-ba95-11ea-850a-58f2b4af5188:1-6

    # remove relay-dir, now relay starting point(syncer checkpoint) should be 09bec856-ba95-11ea-850a-58f2b4af5188:1-4:6
    # check if relay correctly handle gap
    pkill -hup dm-worker.test 2>/dev/null || true
    check_port_offline $WORKER1_PORT 20
    check_port_offline $WORKER2_PORT 20
    rm -rf $WORK_DIR/worker1/relay_log || true
    rm -rf $WORK_DIR/worker2/relay_log || true
    run_dm_worker $WORK_DIR/worker1 $WORKER1_PORT $cur/conf/dm-worker1.toml
    run_dm_worker $WORK_DIR/worker2 $WORKER2_PORT $cur/conf/dm-worker2.toml
    check_rpc_alive $cur/../bin/check_worker_online 127.0.0.1:$WORKER1_PORT
    check_rpc_alive $cur/../bin/check_worker_online 127.0.0.1:$WORKER2_PORT

    run_dm_ctl $WORK_DIR "127.0.0.1:$MASTER_PORT" \
        "start-task $cur/conf/dm-task.yaml"\
        "\"result\": true" 3
    check_sync_diff $WORK_DIR $cur/conf/diff_config.toml

    # TODO: test if should error, error indeed
}

cleanup_data gtid
# also cleanup dm processes in case of last run failed
cleanup_process $*
run $*
cleanup_process $*

echo "[$(date)] <<<<<< test case $TEST_NAME success! >>>>>>"
