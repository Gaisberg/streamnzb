# Changelog

## [5.1.0](https://github.com/Gaisberg/streamnzb/compare/v5.0.0...v5.1.0) (2026-08-18)


### Features

* **indexer:** per-indexer content scope for anime routing ([36c4318](https://github.com/Gaisberg/streamnzb/commit/36c431883334c5ffa094e86b26cacfb668a083db))
* **metadata:** score-ranked artwork, source-native logos and cast photos ([f67b106](https://github.com/Gaisberg/streamnzb/commit/f67b106c4dc5b5f46b0cd5d2fb2e54489ace60fd))


### Bug Fixes

* **filters:** align Filters UI with the jhin engine and enforce profile invariants ([879d618](https://github.com/Gaisberg/streamnzb/commit/879d618e18f6715e4ed3b85eb41627fc8c68da26))
* **proxy:** stop advertising unimplemented NNTP capabilities and greet with 201 ([287e047](https://github.com/Gaisberg/streamnzb/commit/287e047edda280daafea1c8eceb7008ad7c27567))
* **search:** resolve anime films as movies and validate their results ([a4e4ecf](https://github.com/Gaisberg/streamnzb/commit/a4e4ecfb3cf2d1cb99dd4b2f42e20ebba071cd5c))
* **stremio:** drop blank lines left by false conditionals in result templates ([1960eed](https://github.com/Gaisberg/streamnzb/commit/1960eedb6d22c26a5d6e5102bdd2fdc62d8134e0)), closes [#187](https://github.com/Gaisberg/streamnzb/issues/187)

## [5.0.0](https://github.com/Gaisberg/streamnzb/compare/v4.17.0...v5.0.0) (2026-08-17)


### Features

* **easynews:** search the 3.0 API with server-side filtering and pagination ([1dd514e](https://github.com/Gaisberg/streamnzb/commit/1dd514e590c26e4b0dd305d3b6e57e49f3f8a37e))
* **metadata:** turn StreamNZB into a full Stremio metadata provider ([3921fe4](https://github.com/Gaisberg/streamnzb/commit/3921fe44cd2cdd821096feba373efb0ffe29d706))
* **streams:** allow renaming stream and overriding the addon name ([7f81701](https://github.com/Gaisberg/streamnzb/commit/7f81701b4f2aa6b73476f4d5b0a5e19383674242))


### Bug Fixes

* **frontend:** make stream settings reordering work on touch devices via shared SortableList ([ca5c45a](https://github.com/Gaisberg/streamnzb/commit/ca5c45ae0a25fadd4bb2ac374cfc0e35fc15bc19))
* **frontend:** use useWatch so React Compiler can memoize general settings ([78a0813](https://github.com/Gaisberg/streamnzb/commit/78a08138f75e8cf0572741905d92c88b67266f6d))
* **providers:** stop the SSL switch overlapping its label and move the port with it ([2d2b870](https://github.com/Gaisberg/streamnzb/commit/2d2b870b0dd9eb60e5ec613398036703c574ba8d))
* **speedtest:** sync the gauge, hide phantom suggestions, and measure gigabit lines ([a55c9d9](https://github.com/Gaisberg/streamnzb/commit/a55c9d91efcbb159d9f19af44c86a1191ee83a4a))

## [4.17.0](https://github.com/Gaisberg/streamnzb/compare/v4.16.0...v4.17.0) (2026-08-14)


### Features

* **dashboard:** show per-session downloaded bytes and keep active list order stable ([8795218](https://github.com/Gaisberg/streamnzb/commit/8795218f0d1711fe94bc3e51f85817a238807a3f))
* **indexer:** support query params in newznab URLs and surface Hydra sub-indexer names ([f3ba4d2](https://github.com/Gaisberg/streamnzb/commit/f3ba4d20ff9ea789507efebfe284d32267b0b7db)), closes [#59](https://github.com/Gaisberg/streamnzb/issues/59)
* **providers:** add connection latency and throughput speed tests ([dc93b62](https://github.com/Gaisberg/streamnzb/commit/dc93b62f555c4bcf534ffad7dc995f904da0c45e))
* **streams:** cap playback connections per provider, per stream ([fede8de](https://github.com/Gaisberg/streamnzb/commit/fede8ded571b3a4fd403c3337d7f7be430a942ab))
* **streams:** enable or disable providers per stream ([28917fb](https://github.com/Gaisberg/streamnzb/commit/28917fba5e659bdecc496ed38e2c80d32e94c8d6))

## [4.16.0](https://github.com/Gaisberg/streamnzb/compare/v4.15.0...v4.16.0) (2026-08-10)


### Features

* **history:** search-centric history with funnel diagnostics, plus optional debug stream row ([1ed3dc9](https://github.com/Gaisberg/streamnzb/commit/1ed3dc979d0d2371f4be5ae86416a50ec08ac0af))
* **logging:** add custom log file path via -log-file flag and LOG_PATH env ([8d7c379](https://github.com/Gaisberg/streamnzb/commit/8d7c379a20d5d41a12727914d65255fb2d8cb582))
* **persistence:** add Postgres support alongside SQLite ([7c952f8](https://github.com/Gaisberg/streamnzb/commit/7c952f8ed7a2e08e2e00fd263d5512f5bc75a2b9)), closes [#169](https://github.com/Gaisberg/streamnzb/issues/169)


### Bug Fixes

* **config:** default series search queries to no year ([bf2274e](https://github.com/Gaisberg/streamnzb/commit/bf2274e722edb7358c894f049f2cc07b3c46c589))
* **playback:** stop indexer rate limits and episode-match misses from blacklisting good releases ([ecaa2fa](https://github.com/Gaisberg/streamnzb/commit/ecaa2fab97fbdf73cb2c446b19e72e18e518b517))
* **playback:** stop transient STAT failures from blacklisting good releases ([d237933](https://github.com/Gaisberg/streamnzb/commit/d237933b9756910490533e4c0be66df094b76fe1))
* **stremio:** map Kitsu ids to aired season/episode via anime-lists ([5549045](https://github.com/Gaisberg/streamnzb/commit/5549045cb148f7e4d3c15925e985a91d1a0f6a27))
* **usenet:** fail fast on mid-body connection stalls via rolling read deadline ([2e12bb4](https://github.com/Gaisberg/streamnzb/commit/2e12bb46fb4f73e1e7ffd705bf010777a7a919fc))

## [4.15.0](https://github.com/Gaisberg/streamnzb/compare/v4.14.1...v4.15.0) (2026-08-08)


### Features

* **search:** fold absolute-episode querying into series searches as a default-on supplement ([31ff5dc](https://github.com/Gaisberg/streamnzb/commit/31ff5dca52f6a35be35e601b8426e2742b3dcdb0))


### Bug Fixes

* architecture pass — concurrency fixes, service extraction, dedupe ([7247e56](https://github.com/Gaisberg/streamnzb/commit/7247e562e9eff597fa5233fec2966ba0e5eaf6ed))
* **ui:** show server error text instead of the bare HTTP status ([3dec915](https://github.com/Gaisberg/streamnzb/commit/3dec915f88fcdbc85e305012785941765a4c4937))
* **unpack:** let a continuation RAR volume be scanned on its own ([f1541a6](https://github.com/Gaisberg/streamnzb/commit/f1541a6eb2ac4537161bc3335d9672de2cdf4a85))
* **unpack:** stop blaming PAR2 for archives the scan could not read ([359540e](https://github.com/Gaisberg/streamnzb/commit/359540e552843cd43ab3131a63b89e9853403d42))


### Performance Improvements

* **search:** run combined search requests concurrently ([61c574e](https://github.com/Gaisberg/streamnzb/commit/61c574e5fa599f7403b6e90bca09a17f30bfc929))
* **unpack:** bound the hunt for a nested set's first volume ([913049f](https://github.com/Gaisberg/streamnzb/commit/913049f7dd7e2ec7c6d00c465c16a18ca2ee2c2d))
* **usenet:** probe sampled segments together and stage provider fan-out ([b96d6ef](https://github.com/Gaisberg/streamnzb/commit/b96d6ef05c106e3f8dd78481cbae5a8cfac9852e))

## [4.14.1](https://github.com/Gaisberg/streamnzb/compare/v4.14.0...v4.14.1) (2026-08-07)


### Bug Fixes

* **stremio:** render template list fields without brackets, tolerant helpers ([c4ac3e6](https://github.com/Gaisberg/streamnzb/commit/c4ac3e6293426832963a4842fb9cf18b20e089f0))

## [4.14.0](https://github.com/Gaisberg/streamnzb/compare/v4.13.0...v4.14.0) (2026-08-06)


### Features

* **filters:** per-profile library hit bonus and profile share codes ([11c564a](https://github.com/Gaisberg/streamnzb/commit/11c564aedcee2c890766190168eaac494c1f3a4d))
* **library:** default library search priority to combine library + indexers ([85ec020](https://github.com/Gaisberg/streamnzb/commit/85ec0208b5b981da91c86f6965d31594d81e3cac))
* **search:** absolute-episode series search scope for anime ([354a35c](https://github.com/Gaisberg/streamnzb/commit/354a35c036ca047feee15cad7d09c507fc1c2eb6))
* **settings:** auto-save on change ([b57861f](https://github.com/Gaisberg/streamnzb/commit/b57861fc8c8eda3031d0689f745ca3a7f8b80f1a))
* **stremio:** per-stream result format templates with live preview ([caa7fb5](https://github.com/Gaisberg/streamnzb/commit/caa7fb52b4c8b5dd1e642b377e0c793a70a3662b))
* **stremio:** show stream config name in results and manifest ([e5775a9](https://github.com/Gaisberg/streamnzb/commit/e5775a9373ae0a17659a48077cdf0a8e0231359c))


### Bug Fixes

* **persistence:** eliminate SQLITE_BUSY bursts during playback ([11215ca](https://github.com/Gaisberg/streamnzb/commit/11215ca8d9be8528cbbc7cff27412ee5e7ffd954))
* **streams:** seed indexer overrides for auto-added indexers so they are actually queried ([3681e75](https://github.com/Gaisberg/streamnzb/commit/3681e75a213b31d8e5a275c5f1f8159729b9f31d))
* **stremio:** tie playlist/raw-search cache TTL to session TTL settings ([24a466a](https://github.com/Gaisberg/streamnzb/commit/24a466ab54cff03bfcfb92a94f21bea2a2597a91))

## [4.13.0](https://github.com/Gaisberg/streamnzb/compare/v4.12.0...v4.13.0) (2026-08-04)


### Features

* embedded ffprobe, deep release validation, and self-healing release library ([66a3792](https://github.com/Gaisberg/streamnzb/commit/66a37920ace2b10829d0cb84b3953aadf209929a))
* **stremio:** implement sequential pre-probing failover and fix Kitsu anime episode searches ([d62a466](https://github.com/Gaisberg/streamnzb/commit/d62a466dbf8441de14f3156ad545042fedfe5adb))

## [4.12.0](https://github.com/Gaisberg/streamnzb/compare/v4.11.1...v4.12.0) (2026-08-03)


### Features

* **config:** support custom config path via --config / -c CLI flags and CONFIG_PATH env var ([3f732c1](https://github.com/Gaisberg/streamnzb/commit/3f732c14d0c7ea9447dc2dda9edb779ec3131afd))
* **frontend:** overhaul stats page date range filter with radix popover ([07843df](https://github.com/Gaisberg/streamnzb/commit/07843df298fbaab467bb62c2633d03a98a666292))
* **settings:** move AvailNZB reported-bad filtering from global setting to per-stream toggle ([b3866fc](https://github.com/Gaisberg/streamnzb/commit/b3866fcbe6bac93a4da19c212db149367b432174))
* **stremio:** include jhin score in stream details description during standalone filtering ([eb916ce](https://github.com/Gaisberg/streamnzb/commit/eb916ce64b41f2ed1a8248b7a792561e73af776d))
* **unpack:** extend multi-volume RAR archive handling ([34bc7aa](https://github.com/Gaisberg/streamnzb/commit/34bc7aa9b95cef1a635008aee1bd695a7731d738))


### Bug Fixes

* **frontend:** restore Direct Play link to sidebar navigation ([bcd78ff](https://github.com/Gaisberg/streamnzb/commit/bcd78ffe53ef0d85b62b89fdeb401ac36dc6d2e6))
* **metrics:** average non-zero indexer response times in range summary query ([3eeb0bc](https://github.com/Gaisberg/streamnzb/commit/3eeb0bc6f0c7db059ab5bf35f70dc9fbd36ae1b8))
* **stats:** preserve non-zero indexer response times and compute relative provider usage ([2ffc55b](https://github.com/Gaisberg/streamnzb/commit/2ffc55b39bef2572490de704effa50674f483772))
* **stremio:** preserve season=0 for movie AvailNZB release queries ([97cdebd](https://github.com/Gaisberg/streamnzb/commit/97cdebd34f7c19265482be14f7a0c33d1e0f0b5c))


### Performance Improvements

* **persistence:** index-filter provider and indexer metrics queries by baseline timestamp to eliminate full table scans ([0811543](https://github.com/Gaisberg/streamnzb/commit/0811543f2e674297ddf17671276d737c988e773a))
* **stremio:** parallelize metadata sub-requests and AvailNZB pre-check ([4330934](https://github.com/Gaisberg/streamnzb/commit/433093416195e20969ea4c43bad77e78e90d4e77))
* **stremio:** reuse probed stream and cache segment stat check on play startup ([74ee853](https://github.com/Gaisberg/streamnzb/commit/74ee8535f0dc2b53af1232c86149f36f5636acf9))
* **unpack:** eliminate multi-volume RAR startup delay and optimize continuation probing ([07ad4fa](https://github.com/Gaisberg/streamnzb/commit/07ad4fa537623abb890ac914d6ce0f32745d4be7))

## [4.11.1](https://github.com/Gaisberg/streamnzb/compare/v4.11.0...v4.11.1) (2026-08-02)


### Bug Fixes

* **filters:** make profiles global and persist attribute resets ([561679a](https://github.com/Gaisberg/streamnzb/commit/561679a780796f6d8fb0de5f643093784aae2669))
* **stremio:** resolve TVDB IDs, add direct TVDB metadata fallback, and update manifest types ([c41a41c](https://github.com/Gaisberg/streamnzb/commit/c41a41cd639528bda06c8cd7655a4189c5e957f8))

## [4.11.0](https://github.com/Gaisberg/streamnzb/compare/v4.10.0...v4.11.0) (2026-07-29)


### Features

* **frontend:** retain active page on browser refresh using URL hash navigation ([84d33f7](https://github.com/Gaisberg/streamnzb/commit/84d33f74f105068befb45386606de042e7c0a848))
* **ui/filters:** add language combobox search, preferred language scoring slider, and clean linting ([eafcca9](https://github.com/Gaisberg/streamnzb/commit/eafcca9b415aacdf8a9f3e3e4d2e51835c2e43e3))

## [4.10.0](https://github.com/Gaisberg/streamnzb/compare/v4.9.0...v4.10.0) (2026-07-28)


### Features

* **api:** explain how a profile scores a release ([3eb27cc](https://github.com/Gaisberg/streamnzb/commit/3eb27cc74584c7192586cdcfad96821f97215d61))
* **config:** store a ranking profile on filter profiles ([1e78763](https://github.com/Gaisberg/streamnzb/commit/1e787636f4d0fb1ba74a10e00cc23fbac652a2a1))
* **filters:** ship a permissive default profile ([1e0ab16](https://github.com/Gaisberg/streamnzb/commit/1e0ab168adff08902ad05f2831d7f154c8890eae))
* **parser:** replace go-ptt with jhin ([84c48d2](https://github.com/Gaisberg/streamnzb/commit/84c48d25aa779fe076d0b63ce24a4c9937c1fca2))
* **ranking:** decide eligibility, score and order in one engine ([900402f](https://github.com/Gaisberg/streamnzb/commit/900402ffaf95bd5bf4f1775bb854b7d2fe1113b5))
* **search/ui:** simplify ranking sorting, restructure sidebar, and improve settings deletion ([a16af18](https://github.com/Gaisberg/streamnzb/commit/a16af18feb20fea98f2fb46851ea759c65459365))
* **stremio:** recognise anime from metadata, not just Kitsu ([6a7093b](https://github.com/Gaisberg/streamnzb/commit/6a7093b8f6a6dddaed3f9880a04e5c7676f1fe30))
* **ui:** give filters their own section ([ba80c19](https://github.com/Gaisberg/streamnzb/commit/ba80c19a31258d4a1d9e77b15a7dc15a8993c124))
* **ui:** let preferences carry their own weight ([11dbe4c](https://github.com/Gaisberg/streamnzb/commit/11dbe4c8ed2a5587f0bb0fa060d93e5512e6b636))


### Bug Fixes

* address review findings on filter profiles ([ce1478b](https://github.com/Gaisberg/streamnzb/commit/ce1478be7be4fb59915e6a6cca57fde3ba60bf25))
* **api:** push the config when streams change ([52e1960](https://github.com/Gaisberg/streamnzb/commit/52e19607ddb5f06573cd47d17480c88e189e0074))
* **api:** return the per-content-type bindings with a stream ([a5665ac](https://github.com/Gaisberg/streamnzb/commit/a5665ac576a9b48deed051481e52960a1205d379))
* **config:** clear deleted filter profiles from the streams using them ([4474d6f](https://github.com/Gaisberg/streamnzb/commit/4474d6fc6ab60cf9da2b4e0386478354316fad93))
* **streams:** follow profile references on rename and validate them ([e82f3d0](https://github.com/Gaisberg/streamnzb/commit/e82f3d05b7c6b30a7c1920f46e1b249de83aa2bc))
* **streams:** persist the per-content-type profile bindings ([23d4a02](https://github.com/Gaisberg/streamnzb/commit/23d4a02b20227ce4862bf8bc25e8b677300e78d5))
* **stremio:** compile filter profiles when the server starts ([07fcb07](https://github.com/Gaisberg/streamnzb/commit/07fcb07ac619c8b4bed96c51b24887e049fa5238))
* **ui:** stop carrying pre-migration fields onto new profiles ([8623860](https://github.com/Gaisberg/streamnzb/commit/8623860235731fe8c760923118b6c22f903e3b63))
* **usenet/nntp:** reset speed baseline on polling gap to prevent initial frontend speed spike ([15657bd](https://github.com/Gaisberg/streamnzb/commit/15657bd205b1f12103a7d55effeb3e7906e07b96))


### Performance Improvements

* **ranking:** parse each release once per playlist ([c7d1013](https://github.com/Gaisberg/streamnzb/commit/c7d10133ba907c9f4aba1dd1cd8f3389193a03b7))

## [4.9.0](https://github.com/Gaisberg/streamnzb/compare/v4.8.0...v4.9.0) (2026-07-22)


### Features

* **filters:** add language filters/sorting with alias expansion and fix 4K resolution matching ([db7ca7d](https://github.com/Gaisberg/streamnzb/commit/db7ca7de35b1505e96af1b76b103acec0e99e6ab))
* **indexer:** add shared indexer query cache and unify filter settings UI ([1af0617](https://github.com/Gaisberg/streamnzb/commit/1af06174d2cc6df598ab395ee476f8234c06a1b0))
* **playback:** add option to mute ´Failed to start video´ playback error stream ([de4e899](https://github.com/Gaisberg/streamnzb/commit/de4e899287c9f0baec63d3ba9f4dcbc8872396bc))
* **playback:** implement speculative pre-probing to reduce cold-start latency ([6aa26bf](https://github.com/Gaisberg/streamnzb/commit/6aa26bf3ad0fee5c49d6ad9200ed15e83b9475b3))
* standalone filtering vol1 ([262b072](https://github.com/Gaisberg/streamnzb/commit/262b072263901025c8a3f2a2e6de73b4abe290bf))
* **stremio:** add Kitsu anime support, streamline search queries, and isolate provider demotions ([d716e24](https://github.com/Gaisberg/streamnzb/commit/d716e24fb1fea9928eb9d2cf4a22ae38c8389220))
* **unpack:** add targeted Volume 1 PAR2 repair and nested RAR outer volume fallback ([d6d9bb5](https://github.com/Gaisberg/streamnzb/commit/d6d9bb5460e43d6fe3211b65d6cb3ce25b129dda))


### Bug Fixes

* **api:** persist and return filter_profile_name for stream configs ([70e3075](https://github.com/Gaisberg/streamnzb/commit/70e30753f3402aa68ec8c4aac79674203cc4c146))
* **session:** prevent premature idle eviction of played sessions ([ef23cc5](https://github.com/Gaisberg/streamnzb/commit/ef23cc597b1744ce087f003142d17a2c0d158bba))
* **unpack:** improve RAR volume scan error diagnostics and missing first-volume detection ([2e7187d](https://github.com/Gaisberg/streamnzb/commit/2e7187dd234565ed78caa3aad6f9242288aeb59e))
* validate metadata tokens on save to avoid bad configuration ([2a937f5](https://github.com/Gaisberg/streamnzb/commit/2a937f53be535d9e31d60e10285ea31af403054e))


### Performance Improvements

* **nzb:** optimize NZB parsing with context support, caching, and partial parser ([cdbd373](https://github.com/Gaisberg/streamnzb/commit/cdbd373e7f1d714b634a9e7572512299bf0c37d3))

## [4.8.0](https://github.com/Gaisberg/streamnzb/compare/v4.7.0...v4.8.0) (2026-06-23)


### Features

* **session:** make session inactive and paused playback TTLs configurable ([d9611fd](https://github.com/Gaisberg/streamnzb/commit/d9611fdfa0858fca4e2c6aac7986d36f5b987bd3))
* **unpack:** implement seekable CBC decryption and fix volume offset… ([#154](https://github.com/Gaisberg/streamnzb/issues/154)) ([1ad148f](https://github.com/Gaisberg/streamnzb/commit/1ad148f0970cfe855ffdbcede6528e2b5e1fa023))
* **usenet:** implement defensive 430 provider backoff and demotion ([6b9ffa0](https://github.com/Gaisberg/streamnzb/commit/6b9ffa077bb8f0e7b53ea6f3d0467b9b34ceafd5))


### Bug Fixes

* **media/unpack:** prevent hang in scanVolumesParallel when context is cancelled ([a24620f](https://github.com/Gaisberg/streamnzb/commit/a24620fe64d96063be550fdfe033c123cacd691a))
* **media:** attempt resolve lock starvation and context-cancel hangs during RAR playback ([df06c8f](https://github.com/Gaisberg/streamnzb/commit/df06c8faec8393d8f159382cf06fc6e15ed8518a))
* **playback:** prevent timeouts and concurrent cancellations from suppressing failover ([bd2d638](https://github.com/Gaisberg/streamnzb/commit/bd2d6388f32abde383f424385f1a6c478b4f115d))
* **search:** filter out unplayable full disc rips and ISO releases ([0541fe6](https://github.com/Gaisberg/streamnzb/commit/0541fe6f05f5750742fb10be50ac4f687f5b891b))


### Performance Improvements

* **loader:** optimize multivolume archive playback priming and prefetch lifecycles ([3719980](https://github.com/Gaisberg/streamnzb/commit/371998063c9e0a17cdeed51fe07468c3a80c741d))

## [4.7.0](https://github.com/Gaisberg/streamnzb/compare/v4.6.0...v4.7.0) (2026-06-01)


### Features

* **settings:** allow renaming configured providers and indexers ([f698a18](https://github.com/Gaisberg/streamnzb/commit/f698a18354c8b6e88198a8a0a6793f2eab5b7a7c))
* **stats:** article and unique indexer hits ([82eefe3](https://github.com/Gaisberg/streamnzb/commit/82eefe3df551dc65ad7fda2f9f2b52689612c483))
* **ui:** add full-screen Plyr overlay for direct NZB playback ([a0d3142](https://github.com/Gaisberg/streamnzb/commit/a0d31423cb517271982d8d5fbe969b32d4fb0553))


### Bug Fixes

* **frontend:** download logs with authenticated fetch ([f5f021d](https://github.com/Gaisberg/streamnzb/commit/f5f021da794d477e80b51de496c6d3a0e7f6f700))

## [4.6.0](https://github.com/Gaisberg/streamnzb/compare/v4.5.0...v4.6.0) (2026-05-22)


### Features

* **playback:** recover play requests after cache/session eviction ([0834fca](https://github.com/Gaisberg/streamnzb/commit/0834fcaca8cbdb93229309487202354ecbdabb1d))


### Bug Fixes

* **ci:** pass release metadata to Discord notify on workflow_run ([aa34865](https://github.com/Gaisberg/streamnzb/commit/aa3486566ae3ebf19e29bd1bbd138a6db5ae7f2e))

## [4.5.0](https://github.com/Gaisberg/streamnzb/compare/v4.4.0...v4.5.0) (2026-05-22)


### Features

* **indexers:** support per-indexer user agents ([#144](https://github.com/Gaisberg/streamnzb/issues/144)) ([167f5fc](https://github.com/Gaisberg/streamnzb/commit/167f5fc66421f922f8549e1de77d3a2937cb2493))
* **streaming:** improve usenet resilience with rar scan fallback, pa… ([#145](https://github.com/Gaisberg/streamnzb/issues/145)) ([605551d](https://github.com/Gaisberg/streamnzb/commit/605551da1b0ea247269b45a4785f21e1313ccd86))
* **streams:** add automatic provider/indexer sync with enabled-only enforcement ([0bfd6b4](https://github.com/Gaisberg/streamnzb/commit/0bfd6b4c2f24680bb3f54ce6a3346c4037735973)), closes [#133](https://github.com/Gaisberg/streamnzb/issues/133)

## [4.4.0](https://github.com/Gaisberg/streamnzb/compare/v4.3.0...v4.4.0) (2026-05-15)


### Features

* indexer proxy ([930e744](https://github.com/Gaisberg/streamnzb/commit/930e744e63a126373c4788a65791e948decb526b))
* **indexers:** add NZBHydra2 search results cachetime setting and API parameter ([0fba48d](https://github.com/Gaisberg/streamnzb/commit/0fba48dac6363395b9c292381516d5fba2a2f20c))


### Bug Fixes

* **availnzb:** make reported-bad filtering configurable ([4867cce](https://github.com/Gaisberg/streamnzb/commit/4867cce5ceef88ef0a61076fde367aad8236c671))
* **stremio:** include indexer name in AIO stream descriptions ([48607d3](https://github.com/Gaisberg/streamnzb/commit/48607d3375a15c7f0de0a013775fe89e262fe8df))
* **validation:** avoid stale proxy fallback and parallelize global proxy probes ([1079fc6](https://github.com/Gaisberg/streamnzb/commit/1079fc646e45fa1b8fe634c971ff4da07d642265))

## [4.3.0](https://github.com/Gaisberg/streamnzb/compare/v4.2.0...v4.3.0) (2026-05-14)


### Features

* **stats:** add persisted history metrics API and interactive statistics dashboard ([8091331](https://github.com/Gaisberg/streamnzb/commit/8091331454620e6044978576e7cbfbc33aeaf19a))


### Bug Fixes

* **availnzb:** report definitive unavailable releases across all providers and fix unavailable discard stats tracking ([d922a9d](https://github.com/Gaisberg/streamnzb/commit/d922a9d6e938a88778a764513c88cc1cc311f251))

## [4.2.0](https://github.com/Gaisberg/streamnzb/compare/v4.1.0...v4.2.0) (2026-04-23)


### Features

* **metadata:** improve anime title resolution and validation ([#124](https://github.com/Gaisberg/streamnzb/issues/124)) ([26f878c](https://github.com/Gaisberg/streamnzb/commit/26f878ccd73427236a26fa0939d1d64ceaa5386b))
* **search:** add multilingual title validation ([#126](https://github.com/Gaisberg/streamnzb/issues/126)) ([06ae4b7](https://github.com/Gaisberg/streamnzb/commit/06ae4b7697ea2b1572542dbdade0023e2843000b))


### Bug Fixes

* **frontend:** logout on expired admin auth ([97dd360](https://github.com/Gaisberg/streamnzb/commit/97dd360d121e2088a77dc27e530fc5cab706a7ea))
* **playback:** refresh deferred sessions and dedupe easynews queries ([1ebc5ea](https://github.com/Gaisberg/streamnzb/commit/1ebc5eac3963df2359a4390378e36a5e5893344a))
* **session:** avoid replacing sessions during startup ([b3190b1](https://github.com/Gaisberg/streamnzb/commit/b3190b19609a8dc04d04b22cb9c22a494f28a640))
* **session:** tighten startup locking and logout path ([51f1347](https://github.com/Gaisberg/streamnzb/commit/51f134770bc39c6ada9cf32c89250a6b31ff43ec))

## [4.1.0](https://github.com/Gaisberg/streamnzb/compare/v4.0.2...v4.1.0) (2026-04-16)


### Features

* **availnzb:** simplify ON/OFF mode and automate recover-first key lifecycle ([ec6fc87](https://github.com/Gaisberg/streamnzb/commit/ec6fc87a18d99b0148a400f72d28feed50690094))


### Bug Fixes

* **playback:** refine failover reporting and startup handling ([#120](https://github.com/Gaisberg/streamnzb/issues/120)) ([52ffa2a](https://github.com/Gaisberg/streamnzb/commit/52ffa2acd96bea7f2ff5ed6bf8ce012523daed42))

## [4.0.2](https://github.com/Gaisberg/streamnzb/compare/v4.0.1...v4.0.2) (2026-04-15)


### Bug Fixes

* address follow-up review feedback ([c70b442](https://github.com/Gaisberg/streamnzb/commit/c70b442d606151879147c8895e361e07d2eba3f4))
* **availnzb:** only latch successful good reports ([230e094](https://github.com/Gaisberg/streamnzb/commit/230e0941146ea7bb48188fb94fa663817b4b2c6f))
* **persistence:** archive recovered misplaced databases ([675e8b0](https://github.com/Gaisberg/streamnzb/commit/675e8b0cb2d62429e3c8c53d0f48d3d172cdb3b8))
* **persistence:** recover nzb history after db path drift ([b963c85](https://github.com/Gaisberg/streamnzb/commit/b963c85a97763c78939b48190b56f8417138339c))
* **ui:** refine avail reporting and mobile admin layouts ([65d2226](https://github.com/Gaisberg/streamnzb/commit/65d2226b0c60334c9157ed8f31aa611277a02193))

## [4.0.1](https://github.com/Gaisberg/streamnzb/compare/v4.0.0...v4.0.1) (2026-04-09)


### ⚠ BREAKING CHANGES

* **streams:** refactor stream architecture and admin workflows ([#111](https://github.com/Gaisberg/streamnzb/issues/111))

### Features

* **streams:** refactor stream architecture and admin workflows ([#111](https://github.com/Gaisberg/streamnzb/issues/111)) ([cc19986](https://github.com/Gaisberg/streamnzb/commit/cc199862b7e3d4326627ad7e863d0a0739c86a83))


### Miscellaneous Chores

* release 4.0.1 ([0a4e5f7](https://github.com/Gaisberg/streamnzb/commit/0a4e5f7d82cbc5c912ff96f8793cc8342ef7ef30))

## [4.0.0](https://github.com/Gaisberg/streamnzb/compare/v3.7.0...v4.0.0) (2026-04-09)


### ⚠ BREAKING CHANGES

* **streams:** refactor stream architecture and admin workflows ([#111](https://github.com/Gaisberg/streamnzb/issues/111))
* **streams:** overhaul stream model and settings ui

### Features

* cleanup ui a bit ([5cd8bf4](https://github.com/Gaisberg/streamnzb/commit/5cd8bf49490874fbe87f0baa58f5915f3ce95c43))
* easynews indexer ([416accf](https://github.com/Gaisberg/streamnzb/commit/416accf1a86359ebd21351ea9451feffa65b0199))
* **streams:** improve search handling, caching, and settings UX ([#107](https://github.com/Gaisberg/streamnzb/issues/107)) ([12d6eb9](https://github.com/Gaisberg/streamnzb/commit/12d6eb961903359f367c4caab90ccee5d0a66894))
* **streams:** overhaul stream model and settings ui ([b760493](https://github.com/Gaisberg/streamnzb/commit/b7604930b90a1c807541f2f75148f5b794acc1d6))
* **streams:** refactor stream architecture and admin workflows ([#111](https://github.com/Gaisberg/streamnzb/issues/111)) ([cc19986](https://github.com/Gaisberg/streamnzb/commit/cc199862b7e3d4326627ad7e863d0a0739c86a83))
* toggle nntp proxy on and off (Fixes [#101](https://github.com/Gaisberg/streamnzb/issues/101)) ([b62657b](https://github.com/Gaisberg/streamnzb/commit/b62657b78870ebaabf72dc7f1eeebac5f13e69a1))


### Bug Fixes

* accept 201 greetings response on NNTP connection (Fixes [#104](https://github.com/Gaisberg/streamnzb/issues/104)) ([5cd8bf4](https://github.com/Gaisberg/streamnzb/commit/5cd8bf49490874fbe87f0baa58f5915f3ce95c43))

## [Unreleased]

### ⚠ BREAKING CHANGES

* legacy device entries are no longer migrated into the new stream model

### Features

* **availnzb:** record successful playback reports only after real serving crosses byte or time thresholds
* **history:** add richer AvailNZB reporting details and pending playback classification to NZB attempt details

### Bug Fixes

* **history:** show only serving providers for successful playback attempts and keep short playback sessions pending instead of failed
* **ui:** improve NZB history readability and mobile layouts across dialogs, dashboard cards, stream management, and settings forms

### Notes

* global configuration, providers, and indexers are kept during upgrade
* legacy `devices` / `users` state is reset intentionally
* streams must be recreated in the UI after upgrading from older device-based versions

## [3.7.0](https://github.com/Gaisberg/streamnzb/compare/v3.6.0...v3.7.0) (2026-03-22)


### Features

* **availnzb:** auto-recover API key when IP already has one registered ([acad7f9](https://github.com/Gaisberg/streamnzb/commit/acad7f931e3387cf5d8708d365dbe6d4287e3eab))
* remove redundant indexer settings ([a7827b1](https://github.com/Gaisberg/streamnzb/commit/a7827b172c7e336132751787f7c4ddd0239ca435))
* **search:** add per-indexer toggle for ID and string search methods ([d320d87](https://github.com/Gaisberg/streamnzb/commit/d320d87e2b0883a176fa5808b935103d06a60fcb))


### Bug Fixes

* **media:** remove m2ts and ts from supported video extensions ([1c0969b](https://github.com/Gaisberg/streamnzb/commit/1c0969ba76b71c06d24d42432278d90e6f85498a))
* **nzb:** null bytes in nzb broke parser ([71cb895](https://github.com/Gaisberg/streamnzb/commit/71cb895041cc7ae8ea825bcf7e42d4eca5c7769a))
* **unpack:** single episode releases with obfuscated name were skipped ([71cb895](https://github.com/Gaisberg/streamnzb/commit/71cb895041cc7ae8ea825bcf7e42d4eca5c7769a))

## [3.6.0](https://github.com/Gaisberg/streamnzb/compare/v3.5.5...v3.6.0) (2026-03-19)


### Features

* use AppData/Local/streamnzb as data dir on Windows ([043509b](https://github.com/Gaisberg/streamnzb/commit/043509b9417ea36ae5fd589859d774f27cd64f79))

## [3.5.5](https://github.com/Gaisberg/streamnzb/compare/v3.5.4...v3.5.5) (2026-03-19)


### Bug Fixes

* matching filter availnzb results (bad data) ([718ffcf](https://github.com/Gaisberg/streamnzb/commit/718ffcf54de0f8359a1f894b87c27130a4d1150e))
* remove hardcoded 5 count limit ([cbc7d9c](https://github.com/Gaisberg/streamnzb/commit/cbc7d9cf1b311e9ef7056cd735b3807cb1af08cf))


### Performance Improvements

* improve playback performance, dramatically simplify prefetching ([00eff86](https://github.com/Gaisberg/streamnzb/commit/00eff8690be71e28e175779231421136a7303e4f))

## [3.5.4](https://github.com/Gaisberg/streamnzb/compare/v3.5.3...v3.5.4) (2026-03-11)


### Bug Fixes

* fail closed when pack playback cannot match requested episode ([01451ce](https://github.com/Gaisberg/streamnzb/commit/01451ce626bb1856c70fd503870c62ff08ed1c0e))
* logging for production ([a41c14d](https://github.com/Gaisberg/streamnzb/commit/a41c14de93496be6bb4f807a6dc28771ca74efaa))
* **search:** matched hyphens better ( gotta catch em all ) ([b15e677](https://github.com/Gaisberg/streamnzb/commit/b15e6778dd80124257615988f4f6e8ca422f82d6))
* serialize db writes through on shared lock ([d35e0f9](https://github.com/Gaisberg/streamnzb/commit/d35e0f94f9da704ffdcd3ee67f187c7c8c283582))

## [3.5.3](https://github.com/Gaisberg/streamnzb/compare/v3.5.2...v3.5.3) (2026-03-10)


### Bug Fixes

* remove save button from devices that would overwrite config ([f83e96f](https://github.com/Gaisberg/streamnzb/commit/f83e96f5b5d17e9f3c95332ae7a52262e387ad6a))

## [3.5.2](https://github.com/Gaisberg/streamnzb/compare/v3.5.1...v3.5.2) (2026-03-10)


### Bug Fixes

* **availnzb:** make API key registration fail-open during startup ([c6dedbc](https://github.com/Gaisberg/streamnzb/commit/c6dedbcd98980d6af869d898ef8c047195348cc9))


### Performance Improvements

* **unpack:** avoid per-volume segment detection for split 7z parts ([c0612b9](https://github.com/Gaisberg/streamnzb/commit/c0612b905dcbcf293119abfeb4f04e8a9e40cfcf))
* **unpack:** avoid per-volume segment detection when aggregating RAR continuations ([578207b](https://github.com/Gaisberg/streamnzb/commit/578207bb2d003a2c5100f9863400c0387fc94757))

## [3.5.1](https://github.com/Gaisberg/streamnzb/compare/v3.5.0...v3.5.1) (2026-03-09)


### Bug Fixes

* availnzb update ([4c35fcf](https://github.com/Gaisberg/streamnzb/commit/4c35fcf5dafe4df17c76a040017f984ce3923fdf))
* **loader:** use actual last segment size for decoded mapping ([e9bc649](https://github.com/Gaisberg/streamnzb/commit/e9bc6494c848130796d7027220ff91e8bae56646))
* **newznab:** authenticate deferred NZB downloads from keyless grab urls ([0bf62d0](https://github.com/Gaisberg/streamnzb/commit/0bf62d01f777434a542f5957d135f611daba6fea))
* reset daily usage correctly ([1a2bf32](https://github.com/Gaisberg/streamnzb/commit/1a2bf323dfd1a196d93005815f96f78582753607))

## [3.5.0](https://github.com/Gaisberg/streamnzb/compare/v3.4.0...v3.5.0) (2026-03-08)


### Features

* **indexers:** add configurable per-indexer request timeouts ([d733fd9](https://github.com/Gaisberg/streamnzb/commit/d733fd9c5aac637439734ee826a001889f541ec9))
* **troubleshooting:** add log download and bad match report actions ([500f32d](https://github.com/Gaisberg/streamnzb/commit/500f32de5252c9f7fe213c75e4b0241d7f54010a))


### Bug Fixes

* **config:** validate unresolved prowlarr indexer placeholder ([8c3cc1f](https://github.com/Gaisberg/streamnzb/commit/8c3cc1f2076311cbe24690ce34565c1dbb91bfd5))

## [3.4.0](https://github.com/Gaisberg/streamnzb/compare/v3.3.0...v3.4.0) (2026-03-07)


### Features

* episode pack support ([89716d0](https://github.com/Gaisberg/streamnzb/commit/89716d0946dd6afdae2b7166d0d6c35a8a43eecf))
* redact sensitive information from logger ([6b2eda1](https://github.com/Gaisberg/streamnzb/commit/6b2eda179af2666bf2604a8bea987ad7cc086d1f))


### Bug Fixes

* improve search matching even more ([11cd359](https://github.com/Gaisberg/streamnzb/commit/11cd3594c35cbe2d59758124ff3020da7140507c))
* improve search performance some more ([b834342](https://github.com/Gaisberg/streamnzb/commit/b8343420377d46b6a85b56f5e05824a22dff4acd))
* **loader:** prevent seek from canceling active segment reads ([5ab93c2](https://github.com/Gaisberg/streamnzb/commit/5ab93c25048786c61ff91a2d5f38778bc9de4562))


### Performance Improvements

* improve playback and seek performance ([2531fad](https://github.com/Gaisberg/streamnzb/commit/2531fadcd1c53e1758148092da82d60dc1f345eb))
* improve series matching ([e8964e1](https://github.com/Gaisberg/streamnzb/commit/e8964e13034bfa3bc1292413701e94b4a7f1dd7b))

## [3.3.0](https://github.com/Gaisberg/streamnzb/compare/v3.2.0...v3.3.0) (2026-03-05)


### Features

* **availnzb:** add configuration option ([72f0bd6](https://github.com/Gaisberg/streamnzb/commit/72f0bd61f2e7963155d4d22aaf50a4071aad0ca0))
* configurable per device streams ([b852f24](https://github.com/Gaisberg/streamnzb/commit/b852f240ed1d1c52b4797e417822f57f2f0b1dc7))
* nzb history, sqlite persistence, failover race condition fix ([0a48c42](https://github.com/Gaisberg/streamnzb/commit/0a48c424731e8704376fc00dc0d913f3401cb510))


### Bug Fixes

* **search:** tighten fuzzy title match so companion titles don't match main title ([0f54b41](https://github.com/Gaisberg/streamnzb/commit/0f54b41f1532daa71b8ba8a90693c754f8bf3326))


### Performance Improvements

* cap stremio prefetchs ([b2ffd43](https://github.com/Gaisberg/streamnzb/commit/b2ffd437440f5a49e875cae2ada198461ac29010))
* improve failover performance ([0f54b41](https://github.com/Gaisberg/streamnzb/commit/0f54b41f1532daa71b8ba8a90693c754f8bf3326))

## [3.2.0](https://github.com/Gaisberg/streamnzb/compare/v3.1.0...v3.2.0) (2026-03-04)


### Features

* **logging:** rotate to streamnzb.log and add keep-log-files setting ([e2f183b](https://github.com/Gaisberg/streamnzb/commit/e2f183b20da9944c7d91bc4ae92bc46335126f6b))


### Bug Fixes

* fix memory leak, configurable memory limit (512mb default) ([eba23d8](https://github.com/Gaisberg/streamnzb/commit/eba23d8fcd9ee80e4654860273f45897ec86bea8))
* improve search matching using fuzzy matching ([304d78f](https://github.com/Gaisberg/streamnzb/commit/304d78f3508f633f187eba03a0c8d08112845af8))
* **memory:** cap segment cache and expire slotFailedDuringPlayback ([3a44fe0](https://github.com/Gaisberg/streamnzb/commit/3a44fe06123bc8f022c81dc47404d68be730a546))
* **memory:** expire failover order (24h TTL), cap segment size estimator ([65cacbc](https://github.com/Gaisberg/streamnzb/commit/65cacbcf476fd29e040d2fe19ee07d5d0462c92a))
* **session:** evict sessions after max playback duration (Phase 2) ([5a79017](https://github.com/Gaisberg/streamnzb/commit/5a79017999b90917faf799208c09abd6c2c5d4c0))

## [3.1.0](https://github.com/Gaisberg/streamnzb/compare/v3.0.0...v3.1.0) (2026-03-03)


### Features

* nzbhydra and prowlarr are back baby ([5ac0d41](https://github.com/Gaisberg/streamnzb/commit/5ac0d41e46ba363f39e2a5dbf5f663a6b09a1eff))
* **stremio:** STAT first segment before play for faster 430 failover ([5d6f455](https://github.com/Gaisberg/streamnzb/commit/5d6f45594e0f9ab36ad678d674f1681681af90ec))


### Bug Fixes

* **7z:** properly sort 7z volumes passing filenames through ExtractFilename ([18ec2c1](https://github.com/Gaisberg/streamnzb/commit/18ec2c158c9dde02d6d6607ff592565c52c80bb9))
* **aiostreams:** handle failoverorder request from aiostreams ([d5ae1f9](https://github.com/Gaisberg/streamnzb/commit/d5ae1f9e8359530dfa4cb96b23a0e236fbc69128))
* **aiostreams:** user failoverId now correctly maps aiostreams results to streamnzb results ([71535d4](https://github.com/Gaisberg/streamnzb/commit/71535d4093e5835ad568a16785a3ec527b586348))
* remove max size from aiostreams config for now ([ddc5462](https://github.com/Gaisberg/streamnzb/commit/ddc54625a22e9b52641604a9ab82d7ddbbc58b4e))
* **stremio:** include device token in redirect path after fallback ([eb24de9](https://github.com/Gaisberg/streamnzb/commit/eb24de99c04cb51cf4fa36d47e163440a96a8973))
* **stremio:** only add in-range slots to session fallback list ([697c2a2](https://github.com/Gaisberg/streamnzb/commit/697c2a2c7719c5102e81df9d2d7d91d72efc6b83))
* **stremio:** use accurate mime types for fallback streams instead of forced mp4 ([1b2c108](https://github.com/Gaisberg/streamnzb/commit/1b2c108b2fdcfb4085dbd966d5156fc348b4c00c))


### Performance Improvements

* improve failover performance ([7ba6137](https://github.com/Gaisberg/streamnzb/commit/7ba613759c7cf4006d9d4359d3c2b595bea4a1a1))
* **streaming:** parallel first-segment probe for segment 0 ([5808ec4](https://github.com/Gaisberg/streamnzb/commit/5808ec4a6931c4ae459131a664c66436fbe1b5e9))
* **stremio:** prefetch next fallback NZB in background during play ([2739d0e](https://github.com/Gaisberg/streamnzb/commit/2739d0ec05a5cc8c17b5b0dc997c79f6a8b0e012))
* **unpack:** avoid pre-calling EnsureSegmentMap for all volumes in full RAR scan ([c86f231](https://github.com/Gaisberg/streamnzb/commit/c86f23120d05ab4fa5b275fe09d17dd2799110ad))

## [3.0.0](https://github.com/Gaisberg/streamnzb/compare/v2.2.0...v3.0.0) (2026-02-27)


### ⚠ BREAKING CHANGES

* Introducing streams, automatic failover dont sweat when choosing over a release

### Features

* aiostreams support ([2417210](https://github.com/Gaisberg/streamnzb/commit/241721039d0e3cefdfae8032d0e20c5eece497dc))
* Introducing streams, automatic failover dont sweat when choosing over a release ([b92b4dc](https://github.com/Gaisberg/streamnzb/commit/b92b4dcd54f6b638ae4f05da6bafd1ceefe2b477))
* **seek:** experimental seek after failover ([4ba6297](https://github.com/Gaisberg/streamnzb/commit/4ba62979956dc8785b8986f0138c0cd217947fc2))
* show all streams mode for streams ([ebeadf6](https://github.com/Gaisberg/streamnzb/commit/ebeadf63986775b5120e99b7fe223be0095e8dc1))
* **ui:** profile page ([6111940](https://github.com/Gaisberg/streamnzb/commit/6111940a79267b3cde10c8a9ef0531cbdbe9c736))
* **ui:** search page, availnzb changes ([af7733b](https://github.com/Gaisberg/streamnzb/commit/af7733be01cbd8679ccbb7e1fe223d14bff658ac))


### Bug Fixes

* **availnzb:** send NNTP hostnames to AvailNZB, not provider display names ([5c02b85](https://github.com/Gaisberg/streamnzb/commit/5c02b85e438620e413c3ed609d64d8ff8c7f3977))
* **frontend:** normalize checkboxes persist when unchecked ([18622ec](https://github.com/Gaisberg/streamnzb/commit/18622ec0d4e13fb313a55dfd6c1b7ca58bbb07a8))
* **media:** wait for prefetch drain in Seek before returning ([b3e0595](https://github.com/Gaisberg/streamnzb/commit/b3e0595bdfab11cf1b5bc1cccf89fd75b69d4e3a))
* priority overrides availnzb ([8adc873](https://github.com/Gaisberg/streamnzb/commit/8adc8737b9b0d56f052a07edc777af6a5d48abba))
* **search:** filter ID search results by content title/year ([2781f84](https://github.com/Gaisberg/streamnzb/commit/2781f8476177f18e2230cc572ebcb8ee8091e7f1))
* **search:** match titles by letters/digits only in FilterResults ([ec2eb12](https://github.com/Gaisberg/streamnzb/commit/ec2eb1234149a563f8407643d9612ef7aaa21058))
* **stremio:** hide stream configs with no candidates after filtering ([3f5cfd1](https://github.com/Gaisberg/streamnzb/commit/3f5cfd1bce2d0b3a01318344643e70585ccab26a))
* **stremio:** set QuerySource=id for AvailNZB releases so triage doesn't push them to bottom ([4586c02](https://github.com/Gaisberg/streamnzb/commit/4586c02d3a4fda061ccee367d0a5b3bf146a8e32))
* string search as t=search instead of tvsearch as per api specs ([a8613d0](https://github.com/Gaisberg/streamnzb/commit/a8613d01b6fea4a7fa7799cd81e25372d086dc48))
* **triage:** whole-word release group match; paste comma list in UI ([de37698](https://github.com/Gaisberg/streamnzb/commit/de376984231bedb31c15b437b6b4dcc52fa05303))


### Performance Improvements

* improve playback with a buffered response ([8578af6](https://github.com/Gaisberg/streamnzb/commit/8578af6d2e6979656924f8e9e18e4cb5ea9db6cf))

## [2.2.0](https://github.com/Gaisberg/streamnzb/compare/v2.1.0...v2.2.0) (2026-02-25)


### Features

* auto failover configuration option ([a9f62f4](https://github.com/Gaisberg/streamnzb/commit/a9f62f4e9fce8b01f7ef13d53b81f9e740ae1283))
* fallback to next possible stream instead of error ([bcec24b](https://github.com/Gaisberg/streamnzb/commit/bcec24b6b101e7b564ab9209ddfff25ff45dc3e7))
* password protected archive support ([ca4a9de](https://github.com/Gaisberg/streamnzb/commit/ca4a9deafe9beb67b59cb2dc8b4c8dfa9d7b8adc))


### Bug Fixes

* **stremio:** set req.IMDbID when resolving IMDb from TMDB for movie requests ([8996409](https://github.com/Gaisberg/streamnzb/commit/8996409bb7837032f4a909aeccb61f20bf1ab831))

## [2.1.0](https://github.com/Gaisberg/streamnzb/compare/v2.0.0...v2.1.0) (2026-02-24)


### Features

* **availnzb:** use backbones API and status/url, status/imdb, status/tvdb ([21462f0](https://github.com/Gaisberg/streamnzb/commit/21462f03ef88a149146902e4cce8902bf6d2e2b2))
* **indexer:** newznab search feedback fixes (t=search, ID-only q, S01E01, season/ep option) ([b92b9cf](https://github.com/Gaisberg/streamnzb/commit/b92b9cf1a57164d3dc98fb14833aa218ff32764f))
* **indexer:** per-indexer and per-device-per-indexer search settings ([4d8fde1](https://github.com/Gaisberg/streamnzb/commit/4d8fde13a5b90a9b8e6a071286c6fee03d764c9a))
* **search:** add Newznab CAPS discovery, per-indexer categories, and search query terms ([1b3cf47](https://github.com/Gaisberg/streamnzb/commit/1b3cf476c689208646c932d22ed43946fc9ad163))


### Bug Fixes

* comma seperated inputs were not working ([2b14276](https://github.com/Gaisberg/streamnzb/commit/2b1427668de8a5144462b280bb9315817c26d516))
* **search:** make allowed_languages filter work with parser and indexer formats ([d91d03d](https://github.com/Gaisberg/streamnzb/commit/d91d03d3c57ffe34704d5fbe4e1addee135b371e))
* **search:** strict series title filter to avoid wrong show matches ([6ef470c](https://github.com/Gaisberg/streamnzb/commit/6ef470c882edcd6a61a59f9150cb0cb804d23f85))
* **search:** stricter movie text filter by phrase and year ([0be7206](https://github.com/Gaisberg/streamnzb/commit/0be720661121c5b6ef9bed632836a6251bf85ae7))

## [2.0.0](https://github.com/Gaisberg/streamnzb/compare/v1.3.0...v2.0.0) (2026-02-20)


### ⚠ BREAKING CHANGES

* remove nzbhydra and prowlarr

### Features

* add extended BODY+yEnc probe to AvailNZB cache warming ([f60a88d](https://github.com/Gaisberg/streamnzb/commit/f60a88da01b3dd1dc7e02f52bc4169e55f8ff28a))
* **env:** default User-Agent StreamNZB/version for indexer requests (nzbfinder ping required user-agent) ([07f7966](https://github.com/Gaisberg/streamnzb/commit/07f79662401757c1fb72b5fb384367a49b9a5810))
* **indexer:** add possibility to disabled indexers ([07f7966](https://github.com/Gaisberg/streamnzb/commit/07f79662401757c1fb72b5fb384367a49b9a5810))
* rar is back, report all providers as bad etc... ([d3eee06](https://github.com/Gaisberg/streamnzb/commit/d3eee063e4b64f84c8f7da05ecfa23f61e2a2bfc))
* remove nzbhydra and prowlarr ([86a5442](https://github.com/Gaisberg/streamnzb/commit/86a5442aa63f3c0dcebd4e7683ce4ef31708b977))


### Bug Fixes

* daily indexer usage reset doubling counts for unlimited accounts ([484aed1](https://github.com/Gaisberg/streamnzb/commit/484aed16e14b299e18d5962e1f5d79556dd2bb5d))
* improve validation scanning, slower streams but better overall quality ([871a0e2](https://github.com/Gaisberg/streamnzb/commit/871a0e2a86e0f15e472112d8f600a180f38970d4))
* **nzb:** fall back to poster when subject is empty for CompressionType ([181e26f](https://github.com/Gaisberg/streamnzb/commit/181e26f4cf671fa54bca5480cda0696aba7eadd1))
* **unpack:** use exact rardecode PackedSize for RAR continuation volumes ([3de3699](https://github.com/Gaisberg/streamnzb/commit/3de3699145d66cabae4766bbd54e591f7d3fa0f8))

## [1.3.0](https://github.com/Gaisberg/streamnzb/compare/v1.2.0...v1.3.0) (2026-02-18)


### Features

* configurable user agents (see .env.example) ([11aac28](https://github.com/Gaisberg/streamnzb/commit/11aac288c7a50c848bdac08eb1f669c829785b01))
* text query support ([d87f19b](https://github.com/Gaisberg/streamnzb/commit/d87f19be20a4d5b198d936fc179853127d71df17))


### Bug Fixes

* device sorting and filtering defaults ([9aa8969](https://github.com/Gaisberg/streamnzb/commit/9aa8969e100f0e1ac77f5602baebc8831a187491))

## [1.2.0](https://github.com/Gaisberg/streamnzb/compare/v1.1.0...v1.2.0) (2026-02-17)


### Features

* max streams per resolution ([5a111d3](https://github.com/Gaisberg/streamnzb/commit/5a111d314af1a227c3728bc396982199a1d1fdba))
* provider priority support ([a2ad009](https://github.com/Gaisberg/streamnzb/commit/a2ad0093e186f2383d20a7d2987abdd94da94101))


### Bug Fixes

* lets not support rar for now ([13ac20f](https://github.com/Gaisberg/streamnzb/commit/13ac20f39e4a0e92695d99b8eef96266e5d1711c))
* preserve ldflags values ([1cc1f8b](https://github.com/Gaisberg/streamnzb/commit/1cc1f8b564a9b2fa18b4cbe891a273c9c90219e8))


### Performance Improvements

* greatly improve seeking performance with binary search instead of linear search with archived releases ([344d2e4](https://github.com/Gaisberg/streamnzb/commit/344d2e48227a5444b611a675925d3c00902a5014))
* overall stream performance improvements, code quality improvements ([d3a6d3e](https://github.com/Gaisberg/streamnzb/commit/d3a6d3e14de0c56ed2b3e0e6bf7389f1b34f816e))

## [1.1.0](https://github.com/Gaisberg/streamnzb/compare/v1.0.3...v1.1.0) (2026-02-16)


### Features

* configurable admin account (may result in broken manifests, so reinstall the addon) ([3b2dbd4](https://github.com/Gaisberg/streamnzb/commit/3b2dbd44593869efc4b09f4eaa2129163eaeac7e))

## [1.0.3](https://github.com/Gaisberg/streamnzb/compare/v1.0.2...v1.0.3) (2026-02-15)


### Bug Fixes

* nzbhydra and prowlarr indexers ([cd34c58](https://github.com/Gaisberg/streamnzb/commit/cd34c586907644b433b59039ebf9890672b66649))

## [1.0.2](https://github.com/Gaisberg/streamnzb/compare/v1.0.1...v1.0.2) (2026-02-15)


### Bug Fixes

* availnzb changes, much faster results for reported releases ([d5fe21f](https://github.com/Gaisberg/streamnzb/commit/d5fe21ffaae89780704b9fa5a9b1ec3c7cf459cd))
* cleanup() deadlock when expiring a session (pkg/session/manager.go) — fixed (likely root cause) ([41a1316](https://github.com/Gaisberg/streamnzb/commit/41a13168ff96b782a009bd5dfe7c902cb4606c33))
* **loader:** add maximum timeout for segment downloads to prevent worker exhaustion ([387bd54](https://github.com/Gaisberg/streamnzb/commit/387bd540f651714db16bc5539232bc9b2e711465))
* **loader:** add timeout wrapper for decode.DecodeToBytes to prevent blocking ([c784b1f](https://github.com/Gaisberg/streamnzb/commit/c784b1fc71f90bdde64fd513bfbea386bf3dd26c))
* **loader:** cancel downloads for cleared segments to release connections promptly ([8d82f82](https://github.com/Gaisberg/streamnzb/commit/8d82f826512999b183b0cde017645bf3aa7a150a))
* **loader:** discard NNTP client on decode timeout to avoid connection reuse panic ([4fc3653](https://github.com/Gaisberg/streamnzb/commit/4fc3653969db3922a246ced12cd33397f31e37e6))
* **loader:** improve condition variable wait with periodic context checks ([38300cb](https://github.com/Gaisberg/streamnzb/commit/38300cbe0e8a3247a2ea1bbc15c41be55bec41b3))
* **loader:** prevent deadlock and memory leak in SmartStream when paused ([aa339de](https://github.com/Gaisberg/streamnzb/commit/aa339de07c8e82da0a8f24480b105c69855efaee))
* more possible hanging fixes ([47e9e8a](https://github.com/Gaisberg/streamnzb/commit/47e9e8a6bcf3cd5189786f1b79c05a92b16ac742))
* **nntp:** add deadline to body reads to prevent indefinite blocking ([69ba448](https://github.com/Gaisberg/streamnzb/commit/69ba4484f07c2cc064dcfa5e78d6cbcb1fee6817))
* persist env vars on ui changes ([6eab92b](https://github.com/Gaisberg/streamnzb/commit/6eab92ba8a177c0e2fb6a28dc92b9dca597bb6d1))
* prevent hangs and resource exhaustion during long runs ([0ab5bfd](https://github.com/Gaisberg/streamnzb/commit/0ab5bfd8cc335f71d866224196df599ec05415d5))
* **session:** prevent cleanup of sessions with active playback ([abf7c61](https://github.com/Gaisberg/streamnzb/commit/abf7c6194c57bbacf8a24ee11e010ceeafb0240c))
* **stremio:** cancel session context when HTTP request is cancelled ([629861e](https://github.com/Gaisberg/streamnzb/commit/629861ed2b72f1673096f79bbe2f612e3b4ea109))
* **stremio:** implement StreamMonitor.Close() to properly close underlying stream ([1ae7722](https://github.com/Gaisberg/streamnzb/commit/1ae77221ab56f404637d208c67afd8ae2cdd9390))
* various stuff ([560aade](https://github.com/Gaisberg/streamnzb/commit/560aadea27358e9a397742e5a9d7705f4cda89aa))

## [1.0.1](https://github.com/Gaisberg/streamnzb/compare/v1.0.0...v1.0.1) (2026-02-13)


### Bug Fixes

* install button after auth changes ([f60510a](https://github.com/Gaisberg/streamnzb/commit/f60510a71da3db58fedb878b98611297b4768e6f))
* serve embedded failure video instead of big buck bunny ([a7ae387](https://github.com/Gaisberg/streamnzb/commit/a7ae3871e6a566323daea602ee87814fe072bac3))
* use tvdb then tmdb as fallback to enhance queries ([dba103f](https://github.com/Gaisberg/streamnzb/commit/dba103f0aae3b7c88f8525ac1f8e7d6bd8f6d517))

## [1.0.0](https://github.com/Gaisberg/streamnzb/compare/v0.7.0...v1.0.0) (2026-02-13)


### ⚠ BREAKING CHANGES

* login auth, device management with seperate filters and sorting

### Features

* add Easynews indexer support (experimental) ([df3c92a](https://github.com/Gaisberg/streamnzb/commit/df3c92a6ed9b60f9e173e952c92c61aba622f9fa))
* add NZBHydra2 indexer discovery ([df3c92a](https://github.com/Gaisberg/streamnzb/commit/df3c92a6ed9b60f9e173e952c92c61aba622f9fa))
* enforce indexer limits and add persistent provider usage tracking ([7bfa5e8](https://github.com/Gaisberg/streamnzb/commit/7bfa5e874399bd6964e5298ce79490c72edebfe1))
* improve visual tagging filtering (3D) ([82a3b44](https://github.com/Gaisberg/streamnzb/commit/82a3b44658809b66e84a017c697d23505afa42f0))
* **indexer:** internal newznab indexers ([aa24293](https://github.com/Gaisberg/streamnzb/commit/aa242936053be2cd7bfaf2552ea1f3c9137eb42d))
* login auth, device management with seperate filters and sorting ([d6666ed](https://github.com/Gaisberg/streamnzb/commit/d6666ed28fba6a8d3a21b6ee159c6b6feb44f243))
* **ui:** seperate indexer tab, tracking, ui improvements for providers ([aa24293](https://github.com/Gaisberg/streamnzb/commit/aa242936053be2cd7bfaf2552ea1f3c9137eb42d))


### Bug Fixes

* **config:** clear legacy indexer fields when migrated indexers are removed ([211dece](https://github.com/Gaisberg/streamnzb/commit/211dece03246f09f1e2f2dfa8d0dd124889b138a))
* disable auto-scroll to logs section on homepage ([df3c92a](https://github.com/Gaisberg/streamnzb/commit/df3c92a6ed9b60f9e173e952c92c61aba622f9fa))
* migrated prowlarr url ([70c6b71](https://github.com/Gaisberg/streamnzb/commit/70c6b71857b67dae0078754d518e4c3ab60002ad))
* pass admin token to created stream url ([0a521af](https://github.com/Gaisberg/streamnzb/commit/0a521aff9ab7723c576a71c6e612e05f4ea13510))
* respect limits for hydra and prowlarr as well ([6361cb0](https://github.com/Gaisberg/streamnzb/commit/6361cb0c01f262004ba9c448cecb19ee4f7a72c2))
* respect TZ env variable ([d6666ed](https://github.com/Gaisberg/streamnzb/commit/d6666ed28fba6a8d3a21b6ee159c6b6feb44f243))
* **session:** pass context around to stop hanging sessions when closing ([aa24293](https://github.com/Gaisberg/streamnzb/commit/aa242936053be2cd7bfaf2552ea1f3c9137eb42d))
* **validation:** add timeouts to prevent instance hangs ([211dece](https://github.com/Gaisberg/streamnzb/commit/211dece03246f09f1e2f2dfa8d0dd124889b138a))

## [0.7.0](https://github.com/Gaisberg/streamnzb/compare/v0.6.2...v0.7.0) (2026-02-09)


### Features

* filtering with ptt attributes ([6319ac4](https://github.com/Gaisberg/streamnzb/commit/6319ac49c2dc6f0355b9683dc896a673fcf9e5c1))
* **triage:** add release deduplication to eliminate duplicate search results ([83bd249](https://github.com/Gaisberg/streamnzb/commit/83bd24951e83db0da22e8d0e45c6d8eff17b6a8b))
* **ui:** reorganize settings page with tabbed interface, add sorting and max streams ([83bd249](https://github.com/Gaisberg/streamnzb/commit/83bd24951e83db0da22e8d0e45c6d8eff17b6a8b))


### Bug Fixes

* max streams ([739321f](https://github.com/Gaisberg/streamnzb/commit/739321f5dc99e832954135f07845f58c70a742bc))
* **nzbhydra:** resolve actual indexer GUID from internal API ([d15e0bb](https://github.com/Gaisberg/streamnzb/commit/d15e0bb604a6079e77278feb8de6fd14a1032a69))
* **stremio:** ensure failed prevalidations don't prevent trying more releases ([83bd249](https://github.com/Gaisberg/streamnzb/commit/83bd24951e83db0da22e8d0e45c6d8eff17b6a8b))
* **stremio:** show 'Size Unknown' for missing indexer file sizes ([da6c87b](https://github.com/Gaisberg/streamnzb/commit/da6c87b82989cf5ff46810439b9864a3db3b2dd6))
* **triage:** reject unknown resolution/codec when filters are configured ([da6c87b](https://github.com/Gaisberg/streamnzb/commit/da6c87b82989cf5ff46810439b9864a3db3b2dd6))

## [0.6.2](https://github.com/Gaisberg/streamnzb/compare/v0.6.1...v0.6.2) (2026-02-07)


### Miscellaneous Chores

* release 0.6.2 ([e20fd8d](https://github.com/Gaisberg/streamnzb/commit/e20fd8d4ee0748b838384f68c97d252922fd0ab8))

## [0.6.1](https://github.com/Gaisberg/streamnzb/compare/v0.6.0...v0.6.1) (2026-02-07)


### Performance Improvements

* various performance improvement and clarifications, prefer most grabbed releases ([52c2d69](https://github.com/Gaisberg/streamnzb/commit/52c2d690ef92f1bf7aa8a0c54ed03522cc118df0))

## [0.6.0](https://github.com/Gaisberg/streamnzb/compare/v0.5.1...v0.6.0) (2026-02-06)


### Features

* **search:** implement tmdb integration and optimize validation ([27453a5](https://github.com/Gaisberg/streamnzb/commit/27453a5ebeaf01b3ea8dc6d17af75c615661a19b))


### Performance Improvements

* optimize 7z streaming ([4cab433](https://github.com/Gaisberg/streamnzb/commit/4cab433d00bf30d5eb80f58dc4e97452ace5962a))

## [0.5.1](https://github.com/Gaisberg/streamnzb/compare/v0.5.0...v0.5.1) (2026-02-06)


### Bug Fixes

* load correct log level after boot from config ([b1fcbab](https://github.com/Gaisberg/streamnzb/commit/b1fcbabb6be3455899cd3080c61755f166458c04))


### Performance Improvements

* **indexer:** optimize availability checks and implement load balancing ([516d688](https://github.com/Gaisberg/streamnzb/commit/516d68869eeab5f7c50826445958a95ba9a47b84))

## [0.5.0](https://github.com/Gaisberg/streamnzb/compare/v0.4.0...v0.5.0) (2026-02-06)


### Features

* console ui component, included more ui configurations, include nntp proxy in metrics ([0b86f67](https://github.com/Gaisberg/streamnzb/commit/0b86f670bd71afa55e3d7ac27aaea3dc68a720a2))

## [0.4.0](https://github.com/Gaisberg/streamnzb/compare/v0.3.0...v0.4.0) (2026-02-05)


### Features

* **ui:** install on stremio button ([f4ea16d](https://github.com/Gaisberg/streamnzb/commit/f4ea16d0ace287c5e06dc7f751d8032f7694e042))


### Bug Fixes

* default config path to /app/data if folder exists ([226bd79](https://github.com/Gaisberg/streamnzb/commit/226bd79b665f6592b65e697107651d85d8336889))

## [0.3.0](https://github.com/Gaisberg/streamnzb/compare/v0.2.0...v0.3.0) (2026-02-05)


### Features

* **frontend:** implement ui ([e8b80d2](https://github.com/Gaisberg/streamnzb/commit/e8b80d2272a9e6d32e2508155a923016649703e5))


### Bug Fixes

* ensure config saves to loaded path and support /app/data ([a62a7b5](https://github.com/Gaisberg/streamnzb/commit/a62a7b52c153d0108c1dd81bd7c4f65133555a75))
* session keep-alive to show active streams correctly ([32b612a](https://github.com/Gaisberg/streamnzb/commit/32b612ab5ff2db75186448b2f0f6f740d58d5156))


### Performance Improvements

* **backend:** actually utilize multiple connections for streaming ([e8b80d2](https://github.com/Gaisberg/streamnzb/commit/e8b80d2272a9e6d32e2508155a923016649703e5))

## [0.2.0](https://github.com/Gaisberg/streamnzb/compare/v0.1.0...v0.2.0) (2026-02-04)


### Features

* **core:** enhance availability checks and archive scanning ([7beb9ca](https://github.com/Gaisberg/streamnzb/commit/7beb9cad52c046651f5f13830f302afd1595e73a))
* prowlarr indexer support ([82a28ef](https://github.com/Gaisberg/streamnzb/commit/82a28eff052c6248fdfdc9f423cc64eed7bb43b6))
* **unpack:** add heuristic support for obfuscated releases ([e0c606d](https://github.com/Gaisberg/streamnzb/commit/e0c606dacb6d51cf8ba86cc865d3ca2e735d576a))
* **unpack:** implement nested archive support with recursive scanning ([31a65b7](https://github.com/Gaisberg/streamnzb/commit/31a65b7b45e59dd239b9983fd5e1ea64300a507c))


### Bug Fixes

* **loader:** relax seek bounds to support scanner behavior ([31a65b7](https://github.com/Gaisberg/streamnzb/commit/31a65b7b45e59dd239b9983fd5e1ea64300a507c))
* **stremio:** improve error handling, ID parsing, and concurrency ([7beb9ca](https://github.com/Gaisberg/streamnzb/commit/7beb9cad52c046651f5f13830f302afd1595e73a))
* **unpack:** improve file detection and extraction ([31a65b7](https://github.com/Gaisberg/streamnzb/commit/31a65b7b45e59dd239b9983fd5e1ea64300a507c))
* **unpack:** smart selection for 7z archives ([7beb9ca](https://github.com/Gaisberg/streamnzb/commit/7beb9cad52c046651f5f13830f302afd1595e73a))


### Performance Improvements

* **loader:** optimize reading with OpenReaderAt ([31a65b7](https://github.com/Gaisberg/streamnzb/commit/31a65b7b45e59dd239b9983fd5e1ea64300a507c))
* **loader:** optimize stream cancellation and connection usage ([31a65b7](https://github.com/Gaisberg/streamnzb/commit/31a65b7b45e59dd239b9983fd5e1ea64300a507c))

## [0.1.0](https://github.com/Gaisberg/streamnzb/compare/v0.0.2...v0.1.0) (2026-02-04)


### Features

* bootstrapper for startup initialization, give javi11 some recognition in readme ([2cb5cdc](https://github.com/Gaisberg/streamnzb/commit/2cb5cdcd8ea896281f14df0b9f86f08eddaf48e4))

## 0.0.2 (2026-02-04)


### Features

* Initial release ([105d94d](https://github.com/Gaisberg/streamnzb/commit/105d94daba23675d467ea641b23f412199c04102))


### Bug Fixes

* use release-type in release workflow ([c5087c7](https://github.com/Gaisberg/streamnzb/commit/c5087c76b2c9197f6f22b7fb1b5e555f3fc59d1c))


### Miscellaneous Chores

* initial release ([a730030](https://github.com/Gaisberg/streamnzb/commit/a7300307876a0a29bfbfb5067fbf3a538bcc7133))
* Release 0.0.2 ([2281714](https://github.com/Gaisberg/streamnzb/commit/228171467da9c7861d6aa93675b8fb405c245078))

## [0.0.2](https://github.com/Gaisberg/streamnzb/compare/streamnzb-v0.0.1...streamnzb-v0.0.2) (2026-02-04)


### Features

* Initial release ([105d94d](https://github.com/Gaisberg/streamnzb/commit/105d94daba23675d467ea641b23f412199c04102))


### Miscellaneous Chores

* Initial release ([a730030](https://github.com/Gaisberg/streamnzb/commit/a7300307876a0a29bfbfb5067fbf3a538bcc7133))
* Release 0.0.2 ([2281714](https://github.com/Gaisberg/streamnzb/commit/228171467da9c7861d6aa93675b8fb405c245078))

## [0.0.1](https://github.com/Gaisberg/streamnzb/compare/streamnzb-v0.0.1...streamnzb-v0.0.1) (2026-02-04)


### Features

* Initial release ([105d94d](https://github.com/Gaisberg/streamnzb/commit/105d94daba23675d467ea641b23f412199c04102))


### Miscellaneous Chores

* Initial release ([a730030](https://github.com/Gaisberg/streamnzb/commit/a7300307876a0a29bfbfb5067fbf3a538bcc7133))

## 0.0.1 (2026-02-04)


### Features

* Initial release ([105d94d](https://github.com/Gaisberg/streamnzb/commit/105d94daba23675d467ea641b23f412199c04102))


### Miscellaneous Chores

* Initial release ([a730030](https://github.com/Gaisberg/streamnzb/commit/a7300307876a0a29bfbfb5067fbf3a538bcc7133))
