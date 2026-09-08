# kfuse benchmark results

Generated 2026-08-25T23:40:36Z. Latencies in microseconds (µs).

## branch-client

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `branch_wall{try1}` | 1 | 8073054 | 8073054 | 8073054 | 8073054 | 8073054 |
| `branch_wall{try2}` | 1 | 6073968 | 6073968 | 6073968 | 6073968 | 6073968 |
| `branch_wall{try3}` | 1 | 8072576 | 8072576 | 8072576 | 8072576 | 8072576 |

## branch-daemon

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `resume.apply` | 3 | 69 | 57 | 57 | 57 | 102 |
| `resume.image_load` | 3 | 6685 | 6507 | 6507 | 6507 | 7050 |
| `resume.log_read` | 3 | 3337187 | 4003869 | 4003869 | 4003869 | 4004270 |

```json
{"apply_ns":102459,"ev":"resume","from_offset":1821,"had_image":true,"image_load_ns":6506848,"log_read_ns":4003869098,"replayed_events":0,"session":"9e23aeef92b1275ec86182cc39784b4b","ts":1787701156664033800}
{"branch_at":1819,"ev":"branch","image_bytes":253135,"ns":8057508592,"parent":"c27df4232027a044dcdf0d2221536e37","replayed_events":19,"ts":1787701156664043000}
{"apply_ns":47920,"ev":"resume","from_offset":680,"had_image":true,"image_load_ns":7049653,"log_read_ns":2003422591,"replayed_events":0,"session":"0e82e0ae93c66a9cc8d3012b554654da","ts":1787701162739558400}
{"branch_at":1820,"ev":"branch","image_bytes":253135,"ns":6058515278,"parent":"c27df4232027a044dcdf0d2221536e37","replayed_events":19,"ts":1787701162739567400}
{"apply_ns":56752,"ev":"resume","from_offset":4754,"had_image":true,"image_load_ns":6499056,"log_read_ns":4004270273,"replayed_events":0,"session":"f576b33a23251c797d5e31a502c76564","ts":1787701170813888500}
{"branch_at":1820,"ev":"branch","image_bytes":253135,"ns":8057498047,"parent":"c27df4232027a044dcdf0d2221536e37","replayed_events":19,"ts":1787701170813898000}
```

## checkpoint-client

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `checkpoint_wall{files=100}` | 1 | 4039017 | 4039017 | 4039017 | 4039017 | 4039017 |
| `checkpoint_wall{files=400}` | 1 | 4068822 | 4068822 | 4068822 | 4068822 | 4068822 |

## checkpoint-daemon

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `blob_put` | 1317 | 1263 | 1217 | 1550 | 1759 | 6086 |
| `checkpoint.marshal` | 21 | 869 | 735 | 1601 | 1601 | 1773 |
| `checkpoint.upload` | 21 | 1819 | 1779 | 2529 | 2529 | 2663 |
| `commit.create.apply` | 502 | 3 | 3 | 5 | 8 | 106 |
| `commit.create.kafka_append` | 502 | 7007 | 6889 | 7576 | 10198 | 11975 |
| `commit.write.apply` | 1315 | 18 | 13 | 40 | 137 | 342 |
| `commit.write.kafka_append` | 1315 | 7158 | 7034 | 7695 | 10292 | 13769 |
| `fuse_lookup` | 502 | 8 | 7 | 10 | 16 | 22 |
| `fuse_write` | 1317 | 12192 | 14046 | 15657 | 18310 | 22497 |
| `resume.apply` | 2 | 69 | 60 | 60 | 60 | 79 |
| `resume.image_load` | 2 | 1882 | 938 | 938 | 938 | 2827 |
| `resume.log_read` | 2 | 3004083 | 2004417 | 2004417 | 2004417 | 4003750 |

```json
{"apply_ns":59508,"ev":"resume","from_offset":200,"had_image":true,"image_load_ns":2827187,"log_read_ns":4003749841,"replayed_events":1,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701132234207500}
{"ev":"checkpoint","image_bytes":25361,"marshal_ns":160623,"offset":200,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701132236157200,"upload_ns":1778825}
{"apply_ns":79372,"ev":"resume","from_offset":0,"had_image":false,"image_load_ns":937728,"log_read_ns":2004416791,"replayed_events":1,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701126127022000}
{"ev":"checkpoint","image_bytes":12547,"marshal_ns":239669,"offset":99,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701127405147000,"upload_ns":1267154}
{"ev":"checkpoint","image_bytes":25197,"marshal_ns":199290,"offset":199,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701128189839000,"upload_ns":1426488}
{"ev":"checkpoint","image_bytes":37837,"marshal_ns":292568,"offset":299,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701133024113000,"upload_ns":1840799}
{"ev":"checkpoint","image_bytes":50487,"marshal_ns":369384,"offset":399,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701133811246300,"upload_ns":1567296}
{"ev":"checkpoint","image_bytes":63187,"marshal_ns":412809,"offset":499,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701134596808200,"upload_ns":1590952}
{"ev":"checkpoint","image_bytes":75887,"marshal_ns":496817,"offset":599,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701135377284000,"upload_ns":1658390}
{"ev":"checkpoint","image_bytes":126852,"marshal_ns":1341950,"offset":1000,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701142566801200,"upload_ns":2663424}
{"ev":"checkpoint","image_bytes":88587,"marshal_ns":591435,"offset":699,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701136155474700,"upload_ns":1613586}
{"ev":"checkpoint","image_bytes":101287,"marshal_ns":621858,"offset":799,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701136934378000,"upload_ns":1644896}
{"ev":"checkpoint","image_bytes":113987,"marshal_ns":599297,"offset":899,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701137704949800,"upload_ns":1707534}
{"ev":"checkpoint","image_bytes":126688,"marshal_ns":735292,"offset":999,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701138491614500,"upload_ns":1719979}
{"ev":"checkpoint","image_bytes":141926,"marshal_ns":1460627,"offset":1099,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701143295314000,"upload_ns":1747173}
{"ev":"checkpoint","image_bytes":157326,"marshal_ns":1092368,"offset":1199,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701144032172800,"upload_ns":1796843}
{"ev":"checkpoint","image_bytes":169184,"marshal_ns":1147795,"offset":1277,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701144623441700,"upload_ns":2528608}
{"ev":"checkpoint","image_bytes":172726,"marshal_ns":1600836,"offset":1299,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701144774084400,"upload_ns":1819939}
{"ev":"checkpoint","image_bytes":188126,"marshal_ns":1772644,"offset":1399,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701145500582100,"upload_ns":1797219}
{"ev":"checkpoint","image_bytes":203535,"marshal_ns":1167935,"offset":1499,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701146231190000,"upload_ns":1868059}
{"ev":"checkpoint","image_bytes":219035,"marshal_ns":1164653,"offset":1599,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701146963762200,"upload_ns":2022827}
{"ev":"checkpoint","image_bytes":234535,"marshal_ns":1265459,"offset":1699,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701147698931000,"upload_ns":2075442}
{"ev":"checkpoint","image_bytes":250190,"marshal_ns":1509617,"offset":1800,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701148445186600,"upload_ns":2072179}
```

## ckpt-impact-client

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `client_write{writers=2}` | 817 | 14659 | 14379 | 16675 | 18867 | 22642 |

```json
{"elapsed_ns":6011429289,"ev":"throughput","events":817,"events_per_sec":135.91,"label":"writers=2","ts":1787701148584739300,"writers":2}
```

## mem

```json
{"ev":"mem","label":"post-throughput","rss_kb":26824,"ts":1787701124044382700}
{"ev":"mem","label":"files=100","rss_kb":21620,"ts":1787701128198454500}
{"ev":"mem","label":"files=400","rss_kb":22832,"ts":1787701138499225000}
```

## meta-client

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `client_readdir{cold-deep}` | 1 | 1860 | 1860 | 1860 | 1860 | 1860 |
| `client_readdir{cold-wide}` | 1 | 8143 | 8143 | 8143 | 8143 | 8143 |
| `client_readdir{warm-deep}` | 1 | 174 | 174 | 174 | 174 | 174 |
| `client_readdir{warm-wide}` | 1 | 6812 | 6812 | 6812 | 6812 | 6812 |
| `client_stat{cold-deep}` | 1 | 16 | 16 | 16 | 16 | 16 |
| `client_stat{cold-wide}` | 1000 | 3 | 2 | 3 | 7 | 320 |
| `client_stat{warm-deep}` | 1 | 14 | 14 | 14 | 14 | 14 |
| `client_stat{warm-wide}` | 1000 | 2 | 2 | 2 | 8 | 19 |

## meta-daemon

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `fuse_lookup` | 2068 | 3 | 2 | 4 | 12 | 79 |
| `fuse_readdir` | 4 | 328 | 37 | 416 | 416 | 821 |
| `resume.apply` | 1 | 86 | 86 | 86 | 86 | 86 |
| `resume.image_load` | 1 | 924 | 924 | 924 | 924 | 924 |
| `resume.log_read` | 1 | 4007429 | 4007429 | 4007429 | 4007429 | 4007429 |

```json
{"apply_ns":86317,"ev":"resume","from_offset":0,"had_image":false,"image_load_ns":924396,"log_read_ns":4007429357,"replayed_events":1,"session":"39dd335c0ff2b5343f638f80e21a1144","ts":1787701086830847200}
```

## read-client

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `client_read{cold-lower}` | 256 | 2 | 1 | 2 | 22 | 58 |
| `client_read{cold-overlay}` | 256 | 697 | 1 | 6536 | 19466 | 27457 |
| `client_read{warm-lower}` | 256 | 2 | 1 | 2 | 19 | 43 |
| `client_read{warm-overlay}` | 256 | 648 | 2 | 3834 | 19527 | 23184 |

## read-daemon

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `blob_get` | 512 | 962 | 818 | 1669 | 3089 | 8728 |
| `fuse_lookup` | 12 | 6 | 3 | 11 | 11 | 32 |
| `fuse_read[lower]` | 24 | 25 | 26 | 46 | 50 | 84 |
| `fuse_read[overlay]` | 40 | 12601 | 7052 | 31131 | 34090 | 34959 |
| `fuse_readdir` | 4 | 37 | 28 | 39 | 39 | 56 |
| `resume.apply` | 1 | 495 | 495 | 495 | 495 | 495 |
| `resume.image_load` | 1 | 3163 | 3163 | 3163 | 3163 | 3163 |
| `resume.log_read` | 1 | 4003846 | 4003846 | 4003846 | 4003846 | 4003846 |

```json
{"apply_ns":494579,"ev":"resume","from_offset":612,"had_image":true,"image_load_ns":3163154,"log_read_ns":4003846178,"replayed_events":66,"session":"2b644bb72e783301298053d5e0f1f854","ts":1787701081881514800}
```

## readprep-daemon

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `blob_put` | 256 | 1387 | 1307 | 1749 | 2391 | 4615 |
| `checkpoint.marshal` | 2 | 173 | 141 | 141 | 141 | 204 |
| `checkpoint.upload` | 2 | 1580 | 1431 | 1431 | 1431 | 1729 |
| `commit.create.apply` | 4 | 3 | 3 | 3 | 3 | 5 |
| `commit.create.kafka_append` | 4 | 7067 | 6824 | 6933 | 6933 | 7691 |
| `commit.mkdir.apply` | 1 | 3 | 3 | 3 | 3 | 3 |
| `commit.mkdir.kafka_append` | 1 | 7079 | 7079 | 7079 | 7079 | 7079 |
| `commit.rename.apply` | 4 | 7 | 6 | 7 | 7 | 7 |
| `commit.rename.kafka_append` | 4 | 7043 | 7028 | 7083 | 7083 | 7172 |
| `commit.write.apply` | 256 | 11 | 10 | 17 | 36 | 147 |
| `commit.write.kafka_append` | 256 | 7137 | 7044 | 7448 | 8981 | 12727 |
| `fuse_getattr` | 2 | 9 | 8 | 8 | 8 | 10 |
| `fuse_lookup` | 17 | 7 | 8 | 14 | 14 | 17 |
| `fuse_readdir` | 1 | 33 | 33 | 33 | 33 | 33 |
| `fuse_write` | 256 | 8574 | 8414 | 9200 | 12561 | 14544 |
| `resume.apply` | 1 | 79 | 79 | 79 | 79 | 79 |
| `resume.image_load` | 1 | 987 | 987 | 987 | 987 | 987 |
| `resume.log_read` | 1 | 4005251 | 4005251 | 4005251 | 4005251 | 4005251 |

```json
{"apply_ns":79295,"ev":"resume","from_offset":0,"had_image":false,"image_load_ns":987424,"log_read_ns":4005251400,"replayed_events":1,"session":"2b644bb72e783301298053d5e0f1f854","ts":1787701075036138200}
{"ev":"checkpoint","image_bytes":15149,"marshal_ns":203930,"offset":511,"session":"2b644bb72e783301298053d5e0f1f854","ts":1787701076397282300,"upload_ns":1430640}
{"ev":"checkpoint","image_bytes":30402,"marshal_ns":141160,"offset":611,"session":"2b644bb72e783301298053d5e0f1f854","ts":1787701077259122200,"upload_ns":1729166}
```

## resume-client

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `mount_wall{large-image-try1}` | 1 | 4537220 | 4537220 | 4537220 | 4537220 | 4537220 |
| `mount_wall{large-image-try2}` | 1 | 4540530 | 4540530 | 4540530 | 4540530 | 4540530 |
| `mount_wall{large-image-try3}` | 1 | 4544900 | 4544900 | 4544900 | 4544900 | 4544900 |
| `mount_wall{small-noimage-try1}` | 1 | 4534877 | 4534877 | 4534877 | 4534877 | 4534877 |
| `mount_wall{small-noimage-try2}` | 1 | 4534343 | 4534343 | 4534343 | 4534343 | 4534343 |
| `mount_wall{small-noimage-try3}` | 1 | 4534348 | 4534348 | 4534348 | 4534348 | 4534348 |

## resume-daemon

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `resume.apply` | 6 | 958 | 185 | 1552 | 1552 | 2370 |
| `resume.image_load` | 6 | 5276 | 1017 | 8618 | 8618 | 11860 |
| `resume.log_read` | 6 | 4005032 | 4004893 | 4005500 | 4005500 | 4006557 |

```json
{"apply_ns":184795,"ev":"resume","from_offset":0,"had_image":false,"image_load_ns":1005833,"log_read_ns":4005309578,"replayed_events":76,"session":"6ceb4894e91d038a13610c72b9a243d3","ts":1787701180067686400}
{"apply_ns":123781,"ev":"resume","from_offset":0,"had_image":false,"image_load_ns":1003347,"log_read_ns":4004559942,"replayed_events":76,"session":"6ceb4894e91d038a13610c72b9a243d3","ts":1787701184588272000}
{"apply_ns":148979,"ev":"resume","from_offset":0,"had_image":false,"image_load_ns":1016577,"log_read_ns":4003370547,"replayed_events":76,"session":"6ceb4894e91d038a13610c72b9a243d3","ts":1787701189132017200}
{"apply_ns":1551756,"ev":"resume","from_offset":1801,"had_image":true,"image_load_ns":8152708,"log_read_ns":4005499732,"replayed_events":19,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701193688439600}
{"apply_ns":2369689,"ev":"resume","from_offset":1801,"had_image":true,"image_load_ns":8617640,"log_read_ns":4004892812,"replayed_events":19,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701198235262700}
{"apply_ns":1367030,"ev":"resume","from_offset":1801,"had_image":true,"image_load_ns":11860472,"log_read_ns":4006556604,"replayed_events":19,"session":"c27df4232027a044dcdf0d2221536e37","ts":1787701202806549000}
```

## resumeprep-daemon

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `blob_put` | 50 | 1367 | 1256 | 1621 | 1971 | 4556 |
| `commit.create.apply` | 25 | 4 | 3 | 6 | 7 | 9 |
| `commit.create.kafka_append` | 25 | 7099 | 6970 | 7405 | 7911 | 9280 |
| `commit.write.apply` | 50 | 4 | 4 | 7 | 7 | 8 |
| `commit.write.kafka_append` | 50 | 6963 | 6891 | 7194 | 7603 | 8932 |
| `fuse_lookup` | 25 | 9 | 8 | 10 | 10 | 34 |
| `fuse_write` | 50 | 8380 | 8210 | 9013 | 10654 | 11785 |
| `resume.apply` | 1 | 77 | 77 | 77 | 77 | 77 |
| `resume.image_load` | 1 | 1005 | 1005 | 1005 | 1005 | 1005 |
| `resume.log_read` | 1 | 4005553 | 4005553 | 4005553 | 4005553 | 4005553 |

```json
{"apply_ns":77475,"ev":"resume","from_offset":0,"had_image":false,"image_load_ns":1004938,"log_read_ns":4005553302,"replayed_events":1,"session":"6ceb4894e91d038a13610c72b9a243d3","ts":1787701174892756000}
```

## throughput-client

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `client_write{writers=1}` | 936 | 8522 | 8335 | 9109 | 12840 | 19739 |
| `client_write{writers=2}` | 1083 | 14744 | 14474 | 16642 | 20291 | 23531 |
| `client_write{writers=4}` | 1087 | 29379 | 28839 | 33445 | 37274 | 40185 |
| `client_write{writers=8}` | 1086 | 58505 | 58109 | 63820 | 66234 | 67269 |

```json
{"elapsed_ns":8003857542,"ev":"throughput","events":936,"events_per_sec":116.94,"label":"writers=1","ts":1787701099946450400,"writers":1}
{"elapsed_ns":8009872846,"ev":"throughput","events":1083,"events_per_sec":135.21,"label":"writers=2","ts":1787701107958374100,"writers":2}
{"elapsed_ns":8025662813,"ev":"throughput","events":1087,"events_per_sec":135.44,"label":"writers=4","ts":1787701115986429700,"writers":4}
{"elapsed_ns":8053994163,"ev":"throughput","events":1086,"events_per_sec":134.84,"label":"writers=8","ts":1787701124042611700,"writers":8}
```

## throughput-daemon

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `blob_put` | 4192 | 1286 | 1241 | 1556 | 1879 | 6715 |
| `checkpoint.marshal` | 43 | 1703 | 1684 | 3261 | 3439 | 3566 |
| `checkpoint.upload` | 43 | 2489 | 2392 | 3348 | 3776 | 4081 |
| `commit.create.apply` | 15 | 4 | 3 | 6 | 6 | 7 |
| `commit.create.kafka_append` | 15 | 7326 | 7205 | 7831 | 7831 | 8821 |
| `commit.write.apply` | 4192 | 29 | 20 | 62 | 247 | 1597 |
| `commit.write.kafka_append` | 4192 | 7237 | 7120 | 7824 | 10610 | 18232 |
| `fuse_lookup` | 15 | 12 | 12 | 13 | 13 | 18 |
| `fuse_write` | 4192 | 28395 | 28128 | 60298 | 64208 | 67204 |
| `resume.apply` | 1 | 88 | 88 | 88 | 88 | 88 |
| `resume.image_load` | 1 | 899 | 899 | 899 | 899 | 899 |
| `resume.log_read` | 1 | 4005661 | 4005661 | 4005661 | 4005661 | 4005661 |

```json
{"apply_ns":87879,"ev":"resume","from_offset":0,"had_image":false,"image_load_ns":899155,"log_read_ns":4005660963,"replayed_events":1,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701091442286600}
{"ev":"checkpoint","image_bytes":15231,"marshal_ns":188507,"offset":644,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701092800286500,"upload_ns":1394149}
{"ev":"checkpoint","image_bytes":30631,"marshal_ns":114780,"offset":744,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701093652468200,"upload_ns":1541828}
{"ev":"checkpoint","image_bytes":46085,"marshal_ns":175125,"offset":844,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701094510707200,"upload_ns":1587571}
{"ev":"checkpoint","image_bytes":61585,"marshal_ns":180497,"offset":944,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701095368469200,"upload_ns":1659879}
{"ev":"checkpoint","image_bytes":77085,"marshal_ns":484713,"offset":1044,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701096214913500,"upload_ns":1767183}
{"ev":"checkpoint","image_bytes":92585,"marshal_ns":539643,"offset":1144,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701097067094000,"upload_ns":1887048}
{"ev":"checkpoint","image_bytes":108085,"marshal_ns":358605,"offset":1244,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701097916749800,"upload_ns":1618455}
{"ev":"checkpoint","image_bytes":123585,"marshal_ns":383935,"offset":1344,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701098777865500,"upload_ns":1987203}
{"ev":"checkpoint","image_bytes":139085,"marshal_ns":694809,"offset":1444,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701099624003800,"upload_ns":2203899}
{"ev":"checkpoint","image_bytes":154352,"marshal_ns":672779,"offset":1544,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701100404274000,"upload_ns":1679575}
{"ev":"checkpoint","image_bytes":169752,"marshal_ns":918078,"offset":1644,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701101131923000,"upload_ns":2237694}
{"ev":"checkpoint","image_bytes":185152,"marshal_ns":1166782,"offset":1744,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701101867452200,"upload_ns":2038096}
{"ev":"checkpoint","image_bytes":200552,"marshal_ns":682332,"offset":1844,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701102604995800,"upload_ns":2072408}
{"ev":"checkpoint","image_bytes":215952,"marshal_ns":669012,"offset":1944,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701103348193800,"upload_ns":2077000}
{"ev":"checkpoint","image_bytes":231424,"marshal_ns":784054,"offset":2044,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701104088018000,"upload_ns":2135215}
{"ev":"checkpoint","image_bytes":246924,"marshal_ns":1408352,"offset":2144,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701104831425300,"upload_ns":2106153}
{"ev":"checkpoint","image_bytes":262424,"marshal_ns":1707947,"offset":2244,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701105569382700,"upload_ns":2187040}
{"ev":"checkpoint","image_bytes":277924,"marshal_ns":1819132,"offset":2344,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701106310581500,"upload_ns":2144195}
{"ev":"checkpoint","image_bytes":293424,"marshal_ns":1485684,"offset":2444,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701107049250800,"upload_ns":2298211}
{"ev":"checkpoint","image_bytes":308924,"marshal_ns":1684326,"offset":2544,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701107792169200,"upload_ns":2353126}
{"ev":"checkpoint","image_bytes":324026,"marshal_ns":1288976,"offset":2644,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701108532302600,"upload_ns":2392050}
{"ev":"checkpoint","image_bytes":340327,"marshal_ns":1365884,"offset":2750,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701109308830000,"upload_ns":2314390}
{"ev":"checkpoint","image_bytes":355727,"marshal_ns":2068252,"offset":2850,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701110042143000,"upload_ns":2442973}
{"ev":"checkpoint","image_bytes":371127,"marshal_ns":2121714,"offset":2950,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701110776212700,"upload_ns":2537206}
{"ev":"checkpoint","image_bytes":386527,"marshal_ns":1954065,"offset":3050,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701111508483300,"upload_ns":2496422}
{"ev":"checkpoint","image_bytes":401927,"marshal_ns":1473694,"offset":3150,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701112242456000,"upload_ns":2582915}
{"ev":"checkpoint","image_bytes":417327,"marshal_ns":1525618,"offset":3250,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701112977965000,"upload_ns":2646624}
{"ev":"checkpoint","image_bytes":432727,"marshal_ns":1758803,"offset":3350,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701113713961200,"upload_ns":2834770}
{"ev":"checkpoint","image_bytes":448127,"marshal_ns":2154998,"offset":3450,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701114449603300,"upload_ns":2622199}
{"ev":"checkpoint","image_bytes":463530,"marshal_ns":2216500,"offset":3550,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701115184898000,"upload_ns":2688632}
{"ev":"checkpoint","image_bytes":479030,"marshal_ns":2345727,"offset":3650,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701115933874400,"upload_ns":3032879}
{"ev":"checkpoint","image_bytes":492940,"marshal_ns":2533657,"offset":3744,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701116620808000,"upload_ns":2889206}
{"ev":"checkpoint","image_bytes":510388,"marshal_ns":2123163,"offset":3858,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701117461709300,"upload_ns":3091505}
{"ev":"checkpoint","image_bytes":525782,"marshal_ns":3129339,"offset":3958,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701118192841700,"upload_ns":2895344}
{"ev":"checkpoint","image_bytes":541182,"marshal_ns":2950331,"offset":4058,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701118927535400,"upload_ns":3062338}
{"ev":"checkpoint","image_bytes":556582,"marshal_ns":3161019,"offset":4158,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701119660153900,"upload_ns":3111830}
{"ev":"checkpoint","image_bytes":571982,"marshal_ns":3119642,"offset":4258,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701120400625000,"upload_ns":3218725}
{"ev":"checkpoint","image_bytes":587382,"marshal_ns":2952585,"offset":4358,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701121137820700,"upload_ns":3330120}
{"ev":"checkpoint","image_bytes":596160,"marshal_ns":3196779,"offset":4415,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701121558631000,"upload_ns":3347562}
{"ev":"checkpoint","image_bytes":602782,"marshal_ns":3261494,"offset":4458,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701121881286000,"upload_ns":3472734}
{"ev":"checkpoint","image_bytes":618182,"marshal_ns":3565930,"offset":4558,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701122617850400,"upload_ns":3186893}
{"ev":"checkpoint","image_bytes":633582,"marshal_ns":3439202,"offset":4658,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701123361949700,"upload_ns":4081059}
{"ev":"checkpoint","image_bytes":648058,"marshal_ns":3391121,"offset":4752,"session":"ce7a67ef09a77bbad9a04dbb07730e35","ts":1787701124049673700,"upload_ns":3776042}
```

## write4k-client

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `client_create{4k}` | 32 | 7356 | 7184 | 7645 | 8434 | 10594 |
| `client_write{4k}` | 512 | 8639 | 8453 | 9843 | 12486 | 17792 |

## write4k-daemon

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `blob_put` | 512 | 1296 | 1237 | 1602 | 1852 | 4689 |
| `checkpoint.marshal` | 5 | 294 | 241 | 345 | 345 | 438 |
| `checkpoint.upload` | 5 | 1913 | 1774 | 2110 | 2110 | 2528 |
| `commit.create.apply` | 32 | 4 | 3 | 9 | 11 | 13 |
| `commit.create.kafka_append` | 32 | 7167 | 6992 | 7482 | 8103 | 10403 |
| `commit.write.apply` | 512 | 8 | 6 | 13 | 18 | 154 |
| `commit.write.kafka_append` | 512 | 7205 | 7066 | 7774 | 10810 | 16037 |
| `fuse_lookup` | 32 | 11 | 11 | 14 | 14 | 21 |
| `fuse_write` | 512 | 8548 | 8360 | 9450 | 12348 | 17308 |
| `resume.apply` | 1 | 117 | 117 | 117 | 117 | 117 |
| `resume.image_load` | 1 | 918 | 918 | 918 | 918 | 918 |
| `resume.log_read` | 1 | 4004511 | 4004511 | 4004511 | 4004511 | 4004511 |

```json
{"apply_ns":116961,"ev":"resume","from_offset":0,"had_image":false,"image_load_ns":917600,"log_read_ns":4004510869,"replayed_events":1,"session":"709b260c0c19c1caea4765ca3a15e43c","ts":1787701059827112200}
{"ev":"checkpoint","image_bytes":14852,"marshal_ns":218803,"offset":99,"session":"709b260c0c19c1caea4765ca3a15e43c","ts":1787701061192795100,"upload_ns":1448190}
{"ev":"checkpoint","image_bytes":29788,"marshal_ns":437655,"offset":199,"session":"709b260c0c19c1caea4765ca3a15e43c","ts":1787701062043241500,"upload_ns":2110107}
{"ev":"checkpoint","image_bytes":44728,"marshal_ns":241234,"offset":299,"session":"709b260c0c19c1caea4765ca3a15e43c","ts":1787701062904221400,"upload_ns":1704718}
{"ev":"checkpoint","image_bytes":59668,"marshal_ns":227264,"offset":399,"session":"709b260c0c19c1caea4765ca3a15e43c","ts":1787701063758557400,"upload_ns":1773739}
{"ev":"checkpoint","image_bytes":74608,"marshal_ns":345434,"offset":499,"session":"709b260c0c19c1caea4765ca3a15e43c","ts":1787701064613642000,"upload_ns":2527975}
```

## write64k-client

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `client_create{64k}` | 16 | 7323 | 7187 | 7787 | 7787 | 8411 |
| `client_write{64k}` | 128 | 9722 | 9433 | 11070 | 13427 | 16349 |

## write64k-daemon

| series | count | mean | p50 | p95 | p99 | max |
|---|---|---|---|---|---|---|
| `blob_put` | 128 | 2072 | 1970 | 2513 | 3418 | 5080 |
| `checkpoint.marshal` | 1 | 219 | 219 | 219 | 219 | 219 |
| `checkpoint.upload` | 1 | 1544 | 1544 | 1544 | 1544 | 1544 |
| `commit.create.apply` | 16 | 3 | 3 | 6 | 6 | 6 |
| `commit.create.kafka_append` | 16 | 7167 | 7051 | 7648 | 7648 | 8126 |
| `commit.write.apply` | 128 | 6 | 5 | 11 | 15 | 16 |
| `commit.write.kafka_append` | 128 | 7242 | 7085 | 7533 | 9446 | 12941 |
| `fuse_lookup` | 16 | 9 | 9 | 11 | 11 | 14 |
| `fuse_write` | 128 | 9553 | 9329 | 10954 | 12546 | 15047 |
| `resume.apply` | 1 | 71 | 71 | 71 | 71 | 71 |
| `resume.image_load` | 1 | 1045 | 1045 | 1045 | 1045 | 1045 |
| `resume.log_read` | 1 | 4012862 | 4012862 | 4012862 | 4012862 | 4012862 |

```json
{"apply_ns":71385,"ev":"resume","from_offset":0,"had_image":false,"image_load_ns":1045143,"log_read_ns":4012862117,"replayed_events":1,"session":"8e7229927b28461d725408ac60c16754","ts":1787701069091400700}
{"ev":"checkpoint","image_bytes":14746,"marshal_ns":219012,"offset":4055,"session":"8e7229927b28461d725408ac60c16754","ts":1787701070536365000,"upload_ns":1543837}
```
