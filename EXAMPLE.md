```
# nothing is alive

hanwen@hanwen-flow:~/vc/engflow/reftable$ ps -ef | grep horapha |grep  -v grep
(empty)

# Start bazel

hanwen@hanwen-flow:~/vc/engflow/reftable$ HORAPHA_NS=1 horapha  --output_base=$HOME/.cache/horapha  info
Starting local Bazel server and connecting to it...
bazel-bin: /home/hanwen/.cache/horapha/execroot/_main/bazel-out/k8-fastbuild/bin
bazel-genfiles: /home/hanwen/.cache/horapha/execroot/_main/bazel-out/k8-fastbuild/bin
bazel-testlogs: /home/hanwen/.cache/horapha/execroot/_main/bazel-out/k8-fastbuild/testlogs
character-encoding: file.encoding = ISO-8859-1, defaultCharset = ISO-8859-1, sun.jnu.encoding = ISO-8859-1
command_log: /home/hanwen/.cache/horapha/command.log
committed-heap-size: 100MB
execution_root: /home/hanwen/.cache/horapha/execroot/_main
gc-count: 10
gc-time: 16ms
install_base: /home/hanwen/.cache/bazel/_bazel_hanwen/install/15beeda0075f4a09fae0ee246859ebf0
java-home: /usr/lib/jvm/java-21-openjdk-amd64
java-runtime: OpenJDK Runtime Environment (build 21.0.11+10-1-24.04.2-Ubuntu) by Ubuntu
java-vm: OpenJDK 64-Bit Server VM (build 21.0.11+10-1-24.04.2-Ubuntu, mixed mode, sharing) by Ubuntu
local_resources: RAM=59911MB, CPU=16.0
max-heap-size: 15711MB
output_base: /home/hanwen/.cache/horapha
output_path: /home/hanwen/.cache/horapha/execroot/_main/bazel-out
package_path: %workspace%
release: development version
repository_cache: /home/hanwen/.cache/bazel/_bazel_hanwen/cache/repos/v1
server_log: /home/hanwen/.cache/horapha/java.log.hanwen-flow.root.log.java.20260623-183726.11
server_pid: 11
used-heap-size: 45MB
workspace: /home/hanwen/vc/engflow/reftable

# Second run, connects to existing bazel server

hanwen@hanwen-flow:~/vc/engflow/reftable$ HORAPHA_NS=1 horapha  --output_base=$HOME/.cache/horapha  info
bazel-bin: /home/hanwen/.cache/horapha/execroot/_main/bazel-out/k8-fastbuild/bin
bazel-genfiles: /home/hanwen/.cache/horapha/execroot/_main/bazel-out/k8-fastbuild/bin
bazel-testlogs: /home/hanwen/.cache/horapha/execroot/_main/bazel-out/k8-fastbuild/testlogs
character-encoding: file.encoding = ISO-8859-1, defaultCharset = ISO-8859-1, sun.jnu.encoding = ISO-8859-1
command_log: /home/hanwen/.cache/horapha/command.log
committed-heap-size: 100MB
execution_root: /home/hanwen/.cache/horapha/execroot/_main
gc-count: 10
gc-time: 16ms
install_base: /home/hanwen/.cache/bazel/_bazel_hanwen/install/15beeda0075f4a09fae0ee246859ebf0
java-home: /usr/lib/jvm/java-21-openjdk-amd64
java-runtime: OpenJDK Runtime Environment (build 21.0.11+10-1-24.04.2-Ubuntu) by Ubuntu
java-vm: OpenJDK 64-Bit Server VM (build 21.0.11+10-1-24.04.2-Ubuntu, mixed mode, sharing) by Ubuntu
local_resources: RAM=59911MB, CPU=16.0
max-heap-size: 15711MB
output_base: /home/hanwen/.cache/horapha
output_path: /home/hanwen/.cache/horapha/execroot/_main/bazel-out
package_path: %workspace%
release: development version
repository_cache: /home/hanwen/.cache/bazel/_bazel_hanwen/cache/repos/v1
server_log: /home/hanwen/.cache/horapha/java.log.hanwen-flow.root.log.java.20260623-183726.11
server_pid: 11
used-heap-size: 54MB
workspace: /home/hanwen/vc/engflow/reftable


# Do a build.

hanwen@hanwen-flow:~/vc/engflow/reftable$ HORAPHA_NS=1 horapha  --output_base=$HOME/.cache/horapha  build ...
WARNING: Couldn't auto load rules or symbols, because no dependency on module/repository 'protobuf' found. This will result in a failure if there's a reference to those rules or symbols.
WARNING: Couldn't auto load rules or symbols, because no dependency on module/repository 'rules_android' found. This will result in a failure if there's a reference to those rules or symbols.
WARNING: /home/hanwen/vc/engflow/reftable/configs/cc/BUILD:66:19: in cc_toolchain_suite rule //configs/cc:toolchain: Rule is a no-op.
INFO: Analyzed 33 targets (69 packages loaded, 503 targets configured).
INFO: Found 33 targets...
INFO: Elapsed time: 1.159s, Critical Path: 0.04s
INFO: 1 process: 149 action cache hit, 1 internal.
INFO: Build completed successfully, 1 total action


# Checkpoint bazel server, and stop the process.

hanwen@hanwen-flow:~/vc/engflow/reftable$ HORAPHA_NS=1 horapha checkpoint --stop --output_base=$HOME/.cache/horapha  
horapha: requesting checkpoint+stop -> /home/hanwen/.cache/horapha/criu
horapha: checkpoint+stop failed: criu dump: waitid: no child processes


# No bazel anymore.

hanwen@hanwen-flow:~/vc/engflow/reftable$ ps -ef | grep horapha |grep  -v grep
(empty)

# Restore the process

hanwen@hanwen-flow:~/vc/engflow/reftable$ HORAPHA_NS=1 horapha restore --output_base=$HOME/.cache/horapha  
horapha: restoring bazel server (ns pid 11) from /home/hanwen/.cache/horapha/criu
horapha: restored; server reachable at host pid 3458031


# now, Bazel is alive again.

hanwen@hanwen-flow:~/vc/engflow/reftable$ ps -ef | grep horapha |grep  -v grep
hanwen   3458021    3669  0 18:38 pts/10   00:00:00 /home/hanwen/go/bin/horapha __horapha_ns_child__ criu restore --restore-detached --images-dir /home/hanwen/.cache/horapha/criu --ghost-limit 1000000000 --skip-file-rwx-check --shell-job --tcp-close --unprivileged --log-file /home/hanwen/.cache/horapha/criu/criu.log -v4
hanwen   3458031 3458021 13 18:38 ?        00:00:00 bazel(reftable) --add-opens=java.base/java.lang=ALL-UNNAMED -Xverify:none -Djava.util.logging.config.file=/home/hanwen/.cache/horapha/javalog.properties -Dcom.google.devtools.build.lib.util.LogHandlerQuerier.class=com.google.devtools.build.lib.util.SimpleLogHandler$HandlerQuerier -XX:-MaxFDLimit -Djava.lang.Thread.allowVirtualThreads=true -Djava.library.path=/home/hanwen/.cache/bazel/_bazel_hanwen/install/15beeda0075f4a09fae0ee246859ebf0/ -Dfile.encoding=ISO-8859-1 -Duser.country= -Duser.language= -Duser.variant= -Dio.netty.native.deleteLibAfterLoading=false -jar /home/hanwen/.cache/bazel/_bazel_hanwen/install/15beeda0075f4a09fae0ee246859ebf0/A-server.jar --max_idle_secs=0 --noshutdown_on_low_sys_mem --connect_timeout_secs=30 --output_user_root=/home/hanwen/.cache/bazel/_bazel_hanwen --install_base=/home/hanwen/.cache/bazel/_bazel_hanwen/install/15beeda0075f4a09fae0ee246859ebf0 --install_md5=15beeda0075f4a09fae0ee246859ebf0 --output_base=/home/hanwen/.cache/horapha --workspace_directory=/home/hanwen/vc/engflow/reftable --default_system_javabase=/usr/lib/jvm/java-11-openjdk-amd64 --failure_detail_out=/home/hanwen/.cache/horapha/failure_detail.rawproto --idle_server_tasks --write_command_log --nowatchfs --nofatal_event_bus_exceptions --nowindows_enable_symlinks --client_debug=false --server_javabase=/usr/lib/jvm/java-21-openjdk-amd64 --host_jvm_args=-Dio.netty.native.deleteLibAfterLoading=false --product_name=Bazel --option_sources=host_Ujvm_Uargs::max_Uidle_Usecs:/etc/bazel.bazelrc:output_Ubase::server_Ujavabase:

# and it works


hanwen@hanwen-flow:~/vc/engflow/reftable$ HORAPHA_NS=1 horapha  --output_base=$HOME/.cache/horapha   info
bazel-bin: /home/hanwen/.cache/horapha/execroot/_main/bazel-out/k8-fastbuild/bin
bazel-genfiles: /home/hanwen/.cache/horapha/execroot/_main/bazel-out/k8-fastbuild/bin
bazel-testlogs: /home/hanwen/.cache/horapha/execroot/_main/bazel-out/k8-fastbuild/testlogs
character-encoding: file.encoding = ISO-8859-1, defaultCharset = ISO-8859-1, sun.jnu.encoding = ISO-8859-1
command_log: /home/hanwen/.cache/horapha/command.log
committed-heap-size: 251MB
execution_root: /home/hanwen/.cache/horapha/execroot/_main
gc-count: 15
gc-time: 122ms
install_base: /home/hanwen/.cache/bazel/_bazel_hanwen/install/15beeda0075f4a09fae0ee246859ebf0
java-home: /usr/lib/jvm/java-21-openjdk-amd64
java-runtime: OpenJDK Runtime Environment (build 21.0.11+10-1-24.04.2-Ubuntu) by Ubuntu
java-vm: OpenJDK 64-Bit Server VM (build 21.0.11+10-1-24.04.2-Ubuntu, mixed mode, sharing) by Ubuntu
local_resources: RAM=59911MB, CPU=16.0
max-heap-size: 15711MB
output_base: /home/hanwen/.cache/horapha
output_path: /home/hanwen/.cache/horapha/execroot/_main/bazel-out
package_path: %workspace%
release: development version
repository_cache: /home/hanwen/.cache/bazel/_bazel_hanwen/cache/repos/v1
server_log: /home/hanwen/.cache/horapha/java.log.hanwen-flow.root.log.java.20260623-183726.11
server_pid: 11
used-heap-size: 57MB
workspace: /home/hanwen/vc/engflow/reftable


# build something.

hanwen@hanwen-flow:~/vc/engflow/reftable$ HORAPHA_NS=1 horapha  --output_base=$HOME/.cache/horapha test ...
INFO: Analyzed 33 targets (0 packages loaded, 0 targets configured).
INFO: Found 15 targets and 18 test targets...
INFO: Elapsed time: 1.097s, Critical Path: 0.84s
INFO: 19 processes: 36 linux-sandbox.
INFO: Build completed successfully, 19 total actions
//c:basics_test                                                          PASSED in 0.0s
//c:block_test                                                           PASSED in 0.0s
//c:block_test_valgrind_test                                             PASSED in 0.5s
//c:merged_test                                                          PASSED in 0.0s
//c:merged_test_valgrind_test                                            PASSED in 0.5s
//c:pq_test                                                              PASSED in 0.0s
//c:readwrite_test                                                       PASSED in 0.1s
//c:readwrite_test_valgrind_test                                         PASSED in 0.6s
//c:record_test                                                          PASSED in 0.1s
//c:record_test_valgrind_test                                            PASSED in 0.5s
//c:refname_test                                                         PASSED in 0.0s
//c:refname_test_valgrind_test                                           PASSED in 0.5s
//c:stack_test                                                           PASSED in 0.1s
//c:stack_test_valgrind_test                                             PASSED in 0.8s
//c:strbuf_test                                                          PASSED in 0.0s
//c:strbuf_test_valgrind_test                                            PASSED in 0.4s
//c:tree_test                                                            PASSED in 0.0s
//c:tree_test_valgrind_test                                              PASSED in 0.3s

Executed 18 out of 18 tests: 18 tests pass.
There were tests whose specified size is too big. Use the --test_verbose_timeout_warnings command line option to see which ones these are.
```