# Recorded test results

Written by `go run ./tools/record`. Do not edit by hand.

- Version: 0.7.0
- Recorded: 2026-10-21T16:21:29Z
- Machine: linux/amd64, 2 CPUs, go1.24.7

## Tests

442 of 442 tests passed (race detector on).

| Package | Test | Result |
| --- | --- | --- |
| cmd/aftermarket | TestAdaptCheckAndTest | pass |
| cmd/aftermarket | TestAdaptCheckReportsErrorsAndOverlaps | pass |
| cmd/aftermarket | TestAdaptRunNeedsUpstream | pass |
| cmd/aftermarket | TestCheckExample | pass |
| cmd/aftermarket | TestCheckReportsErrorsAndOverlaps | pass |
| cmd/aftermarket | TestConfigCommands | pass |
| cmd/aftermarket | TestGuardCommands | pass |
| cmd/aftermarket | TestHookCommands | pass |
| cmd/aftermarket | TestRecordCheck | pass |
| cmd/aftermarket | TestRecordNeverOverwrites | pass |
| cmd/aftermarket | TestRecordOnBusyPortLeavesNoFile | pass |
| cmd/aftermarket | TestReplayCLI | pass |
| cmd/aftermarket | TestRunNeedsUpstream | pass |
| cmd/aftermarket | TestTestExample | pass |
| cmd/aftermarket | TestTestFailingFixture | pass |
| cmd/aftermarket | TestUsage | pass |
| cmd/aftermarket | TestVersion | pass |
| examples/customer-api | TestContractIsValidAgainstV1 | pass |
| examples/customer-api | TestEveryEndpointIsNeeded | pass |
| examples/customer-api | TestFixtureFilePasses | pass |
| examples/customer-api | TestOldClientPassesThroughAdapt | pass |
| examples/customer-api | TestRecordedSuiteReplaysTheSameThroughAdapt | pass |
| examples/customer-api | TestV2BreaksTheOldClient | pass |
| examples/legacy-admin | TestFixturePasses | pass |
| examples/legacy-admin | TestTheAdminPanelBeforeAndAfter | pass |
| examples/legacy-app | TestFixturePasses | pass |
| examples/legacy-app | TestTheExampleEndToEnd | pass |
| examples/legacy-customer | TestFixtureFilePasses | pass |
| examples/legacy-customer | TestOldClientNeedsTheRules | pass |
| examples/legacy-customer | TestRemovingEitherRuleBreaksTheClient | pass |
| examples/vendor-app | TestFixturePasses | pass |
| examples/vendor-app | TestUpgradeStopsWhereTheVendorChangedSomething | pass |
| examples/vendor-app-toml | TestFixturePasses | pass |
| examples/vendor-app-toml | TestUpgradeStopsAndCommentsStay | pass |
| internal/adapt | TestAuditNeverContainsValues | pass |
| internal/adapt | TestCheckReportsEveryProblemInLineOrder | pass |
| internal/adapt | TestChunkedRequestBodyIsDroppedForGet | pass |
| internal/adapt | TestConcurrencySoak | pass |
| internal/adapt | TestDotSegmentPathsPassThrough | pass |
| internal/adapt | TestDryRunChangesZeroBytes | pass |
| internal/adapt | TestDryRunCoversEveryKindOfChange | pass |
| internal/adapt | TestDryRunNeverTurnsSlowAnswersInto504 | pass |
| internal/adapt | TestExplicitHeadersWinOverRebuildDefaults | pass |
| internal/adapt | TestGzipResponseIsRebuiltAndRecompressed | pass |
| internal/adapt | TestHeaderMapOverlapsAreRejected | pass |
| internal/adapt | TestHeadersRenameAndCredentialsPassUntouched | pass |
| internal/adapt | TestHealthPath | pass |
| internal/adapt | TestHugeNumbersAreRejected | pass |
| internal/adapt | TestInvalidUTF8ParameterIsSkipped | pass |
| internal/adapt | TestLeftoverQuery | pass |
| internal/adapt | TestLeftoverQueryNeverOverridesTranslatedKeys | pass |
| internal/adapt | TestLoadErrors | pass |
| internal/adapt | TestLoadGood | pass |
| internal/adapt | TestMatch | pass |
| internal/adapt | TestMethodChangeAndBodyless | pass |
| internal/adapt | TestMissingFieldsAreLeftOutAndLogged | pass |
| internal/adapt | TestMissingListIsDeduplicated | pass |
| internal/adapt | TestOverlaps | pass |
| internal/adapt | TestQueryStringsKeepTheirBytes | pass |
| internal/adapt | TestRenameNeverOverwritesExistingValues | pass |
| internal/adapt | TestRepeatedParameterChecksHeadersFirst | pass |
| internal/adapt | TestRequestThatCannotBeTranslatedPassesUnchanged | pass |
| internal/adapt | TestResponseIsAllOrNothing | pass |
| internal/adapt | TestResponseSkipLeavesHeadersAlone | pass |
| internal/adapt | TestSkippedMatchThenUpstreamErrorLogsPatternAndSameID | pass |
| internal/adapt | TestSlowUpstreamAndStalledBodyGive504 | pass |
| internal/adapt | TestStalledRequestBodyGets408 | pass |
| internal/adapt | TestTemplateCannotBeBuilt | pass |
| internal/adapt | TestTemplateEach | pass |
| internal/adapt | TestTemplateMissingFieldsAreLeftOut | pass |
| internal/adapt | TestTemplateObject | pass |
| internal/adapt | TestTemplateWithoutBody | pass |
| internal/adapt | TestUnmatchedResponseStreams | pass |
| internal/adapt | TestUnmatchedTrafficIsByteIdentical | pass |
| internal/adapt | TestUnsafeValuesNeverReachThePath | pass |
| internal/adapt | TestUpstreamDownGives502 | pass |
| internal/config | TestApplyTwiceGivesTheSameBytes | pass |
| internal/config | TestDeepNestingIsRefused | pass |
| internal/config | TestEnvFilesKeepEveryLineTheOverlayDoesntChange | pass |
| internal/config | TestEnvRewritesKeepMeaning | pass |
| internal/config | TestFailedWritesLeaveTheOldOutput | pass |
| internal/config | TestFieldsTheOverlayDoesntNameKeepOrderAndValue | pass |
| internal/config | TestFixtureFailuresHideSecrets | pass |
| internal/config | TestFixtures | pass |
| internal/config | TestFormatOf | pass |
| internal/config | TestGuardsStopApply | pass |
| internal/config | TestLoadReportsEveryProblemInLineOrder | pass |
| internal/config | TestRandomDocumentsKeepEverythingElse | pass |
| internal/config | TestRenameIntoItselfIsRefused | pass |
| internal/config | TestSecretsAreNeverPrinted | pass |
| internal/config | TestSecretsStayHiddenWhenMovedOrNamedDifferently | pass |
| internal/config | TestTOMLChangesStayOnTheirLines | pass |
| internal/config | TestTOMLKeepsLineEndings | pass |
| internal/config | TestTOMLRandomChanges | pass |
| internal/config | TestTOMLReads | pass |
| internal/config | TestTOMLRefuses | pass |
| internal/config | TestTOMLRefusesValuesItCantHold | pass |
| internal/config | TestTOMLReviewHandWrittenRandom | pass |
| internal/config | TestTOMLReview_F10_FallbackReorders | pass |
| internal/config | TestTOMLReview_F11_FixtureCantTellDateFromString | pass |
| internal/config | TestTOMLReview_F1_NoopRewritesSpelling | pass |
| internal/config | TestTOMLReview_F1b_FloatBecomesInt | pass |
| internal/config | TestTOMLReview_F2_SecretsInErrors | pass |
| internal/config | TestTOMLReview_F3_DeepNestingCrash | pass |
| internal/config | TestTOMLReview_F3b_InlineNestingQuadratic | pass |
| internal/config | TestTOMLReview_F4_DeleteTableEatsNextComment | pass |
| internal/config | TestTOMLReview_F5_DottedDeleteFallsBackToRewrite | pass |
| internal/config | TestTOMLReview_F6_TextBecomesInvalidDate | pass |
| internal/config | TestTOMLReview_F7_ReaderStrictness | pass |
| internal/config | TestTOMLReview_F8_FinalNewlineAppears | pass |
| internal/config | TestTOMLReview_F9_NewRootKeyAboveHeaderComment | pass |
| internal/config | TestTOMLRewritesWhenItMust | pass |
| internal/config | TestTOMLUnchangedAndTwice | pass |
| internal/config | TestTheBaseIsNeverWritten | pass |
| internal/config | TestTopLevelListDelete | pass |
| internal/config | TestUnchangedFilesComeBackExactly | pass |
| internal/config | TestYAMLColonsAndKeysReadBack | pass |
| internal/config | TestYAMLVendorFile | pass |
| internal/config | TestYAMLWriterReadsBack | pass |
| internal/fixture | TestFixtureErrors | pass |
| internal/fixture | TestFixtureReportsFailures | pass |
| internal/guard | TestAFailureWhileJudgingForwardsNothing | pass |
| internal/guard | TestARequestWithoutAVerdictIsRejected | pass |
| internal/guard | TestAddrKey | pass |
| internal/guard | TestAllowedTrafficPassesByteForByte | pass |
| internal/guard | TestAnOversizedPipelinedHeadIsRejected | pass |
| internal/guard | TestAnUpgradeHeaderAloneKeepsTheChecksOn | pass |
| internal/guard | TestCheckWarnings | pass |
| internal/guard | TestChunkSizesCantHideARequest | pass |
| internal/guard | TestClientsCantChooseTheirIdentityOrAddress | pass |
| internal/guard | TestConcurrentGuessesStayWithinTheLimit | pass |
| internal/guard | TestConnectionLimitsPerSlash64AndProxies | pass |
| internal/guard | TestCredentials | pass |
| internal/guard | TestDangerousConfigs | pass |
| internal/guard | TestDeniedRequestsNeverReachTheApp | pass |
| internal/guard | TestDryRunForwardsAndLogs | pass |
| internal/guard | TestEvictionResetsNoLimit | pass |
| internal/guard | TestFixtureRuns | pass |
| internal/guard | TestH2CNeverTunnelsPastTheRules | pass |
| internal/guard | TestHTTP10WithTransferEncoding | pass |
| internal/guard | TestHealthUpstreamErrorsAndConnections | pass |
| internal/guard | TestIgnoreCaseRejectsNonASCIIPaths | pass |
| internal/guard | TestKeepCredentials | pass |
| internal/guard | TestLimitsHold | pass |
| internal/guard | TestLoadReportsEveryProblemInLineOrder | pass |
| internal/guard | TestLogSummarisesEveryEvent | pass |
| internal/guard | TestLogSummarisesFloods | pass |
| internal/guard | TestLowerCaseMethodsAreRejected | pass |
| internal/guard | TestNoSecretReachesTheLog | pass |
| internal/guard | TestNothingAmbiguousGetsThrough | pass |
| internal/guard | TestOlderAndOddRequestForms | pass |
| internal/guard | TestOptionsStarKeepsVerdictsInStep | pass |
| internal/guard | TestOverrideHeadersNeverReachTheApp | pass |
| internal/guard | TestPathOptionsAndCredentialsInTheQuery | pass |
| internal/guard | TestResponseHeadersAndBodies | pass |
| internal/guard | TestServerHasAWriteTimeout | pass |
| internal/guard | TestSmugglingAfterACleanRequestOnOneConnection | pass |
| internal/guard | TestTLS | pass |
| internal/guard | TestTrackerFollowsSplitReads | pass |
| internal/guard | TestTwoAuthorizationHeaders | pass |
| internal/guard | TestUnderscoreHeadersAreDropped | pass |
| internal/guard | TestUpgrades | pass |
| internal/hook | TestABusyFileDoesNotBlockTheFolder | pass |
| internal/hook | TestAChangeSeenAgainAfterACrashKeepsItsID | pass |
| internal/hook | TestAFileThatBlinksGivesNoEvent | pass |
| internal/hook | TestAQueuedEventWithoutABodyIsRefused | pass |
| internal/hook | TestBaselinesDryRunAndState | pass |
| internal/hook | TestCommandSources | pass |
| internal/hook | TestCommandTimeoutWithAChildHoldingStdout | pass |
| internal/hook | TestDeadLetters | pass |
| internal/hook | TestDeliveryKeepsOrderPerWebhook | pass |
| internal/hook | TestDisplayURLHidesPathsAndQueries | pass |
| internal/hook | TestDryRunPrintsEachChangeOnce | pass |
| internal/hook | TestEveryChangeGivesExactlyOneEvent | pass |
| internal/hook | TestFieldDiffSaysHowManyMore | pass |
| internal/hook | TestFixtureLoading | pass |
| internal/hook | TestFolders | pass |
| internal/hook | TestHTTPSources | pass |
| internal/hook | TestHalfWrittenFilesAreNeverReported | pass |
| internal/hook | TestHelperProcess | pass |
| internal/hook | TestIDsNeverRepeatAcrossBaselines | pass |
| internal/hook | TestLineDiffsKeepSecretsOut | pass |
| internal/hook | TestLoadReportsEveryProblemInLineOrder | pass |
| internal/hook | TestNothingIsLostWhileTheReceiverIsDown | pass |
| internal/hook | TestQueuedEventsKeepTheirBytesAcrossRestarts | pass |
| internal/hook | TestRedactLine | pass |
| internal/hook | TestSecretsStayOut | pass |
| internal/hook | TestSendToNamesEachWebhookOnce | pass |
| internal/hook | TestStoppingMidSendKeepsTheEvent | pass |
| internal/hook | TestTruncatedStructuredFilesWait | pass |
| internal/hook | TestUntilNextWaitsForTheHead | pass |
| internal/ojson | TestCloneIsDeep | pass |
| internal/ojson | TestDecodeKeepsRealReplacementCharacters | pass |
| internal/ojson | TestDecodeRefusesDeepNesting | pass |
| internal/ojson | TestDecodeRejects | pass |
| internal/ojson | TestParsePath | pass |
| internal/ojson | TestPathOps | pass |
| internal/ojson | TestRename | pass |
| internal/ojson | TestRoundTripKeepsOrderAndNumbers | pass |
| internal/patch | Test204AuditSaysNoBody | pass |
| internal/patch | TestAuditNeverContainsValues | pass |
| internal/patch | TestBodiesThatWouldNotRoundTripAreSkipped | pass |
| internal/patch | TestBrokenGzipPassesUnchanged | pass |
| internal/patch | TestChangedBodyDropsTrailersAndDigests | pass |
| internal/patch | TestChunkedResponseIsPatched | pass |
| internal/patch | TestConcurrencySoak | pass |
| internal/patch | TestConnectionListedHeadersAreDroppedWhenARuleMatches | pass |
| internal/patch | TestDryRunChangesZeroBytes | pass |
| internal/patch | TestDryRunNeverTurnsSlowAnswersInto504 | pass |
| internal/patch | TestForwardingHeadersCanBeChanged | pass |
| internal/patch | TestGzipContentLengthIsCorrected | pass |
| internal/patch | TestGzipResponseIsPatchedAndRecompressed | pass |
| internal/patch | TestHealthPath | pass |
| internal/patch | TestMalformedJSONPassesUnchanged | pass |
| internal/patch | TestMalformedRequestBodyPassesUnchanged | pass |
| internal/patch | TestMatchedResponseIsPatched | pass |
| internal/patch | TestNewValidatesUpstream | pass |
| internal/patch | TestNoBodyResponsesAreSkipped | pass |
| internal/patch | TestNullBodyIsUnchangedNotSkipped | pass |
| internal/patch | TestOddQueryStringsPassThroughExactly | pass |
| internal/patch | TestOversizeBodyPassesUnchanged | pass |
| internal/patch | TestPartlyAppliedRuleIsLoggedAsChanged | pass |
| internal/patch | TestPathRulesIgnoreDotSegments | pass |
| internal/patch | TestRequestIsPatched | pass |
| internal/patch | TestRewriteCannotEscapeItsPrefix | pass |
| internal/patch | TestRewriteNeedsItsQueryParameter | pass |
| internal/patch | TestRuleOrderIsDeterministic | pass |
| internal/patch | TestSlowUpstreamGives504 | pass |
| internal/patch | TestStalledBodyGives504 | pass |
| internal/patch | TestStalledRequestBodyGets408 | pass |
| internal/patch | TestUnmatchedResponseStreams | pass |
| internal/patch | TestUnmatchedTrafficIsByteIdentical | pass |
| internal/patch | TestUnsupportedEncodingPassesUnchanged | pass |
| internal/patch | TestUpgradePassesThrough | pass |
| internal/patch | TestUpstreamBasePath | pass |
| internal/patch | TestUpstreamDownGives502 | pass |
| internal/patch | TestUpstreamErrorReasonHasNoValues | pass |
| internal/record | TestAuditIDsAndMaxCases | pass |
| internal/record | TestConcurrentRecording | pass |
| internal/record | TestConfigErrorsAreAllReportedInLineOrder | pass |
| internal/record | TestGzipBodiesAreStoredDecoded | pass |
| internal/record | TestHeadersThatArentTextAreSkipped | pass |
| internal/record | TestHealthPath | pass |
| internal/record | TestJSONIsRedactedWhateverTheContentType | pass |
| internal/record | TestQueryRedactionCoversEverySeparator | pass |
| internal/record | TestQuoteRoundTrips | pass |
| internal/record | TestRecordingChangesZeroBytes | pass |
| internal/record | TestRecordingsRunWithPatchAndAdaptTest | pass |
| internal/record | TestRedaction | pass |
| internal/record | TestRequestWithoutPathIsStoredAsSlash | pass |
| internal/record | TestResponsesStream | pass |
| internal/record | TestUnstorableExchangesPassAndAreSkipped | pass |
| internal/record | TestUpgradedConnectionsPassThrough | pass |
| internal/record | TestUpstreamDownAndSlow | pass |
| internal/record | TestUpstreamURLSecretsStayOut | pass |
| internal/record | TestWhenPicksTrafficAndMaxCasesStops | pass |
| internal/replay | TestBadOptions | pass |
| internal/replay | TestDifferencesAreListedByPath | pass |
| internal/replay | TestDiffsAreCapped | pass |
| internal/replay | TestGzipAnswersAreComparedDecoded | pass |
| internal/replay | TestJSONIsComparedByMeaning | pass |
| internal/replay | TestNothingComparedIsNotOK | pass |
| internal/replay | TestNumbersAreComparedExactly | pass |
| internal/replay | TestOneServiceIsComparedWithTheRecording | pass |
| internal/replay | TestOtherBodiesAreComparedByteForByte | pass |
| internal/replay | TestReportHidesURLSecrets | pass |
| internal/replay | TestReports | pass |
| internal/replay | TestRequestsAreSentAsRecorded | pass |
| internal/replay | TestUnreachableServiceIsAnError | pass |
| internal/replay | TestWritesAreSentOnlyWhenAsked | pass |
| internal/rules | TestGlobMatch | pass |
| internal/rules | TestLoadErrors | pass |
| internal/rules | TestLoadGood | pass |
| internal/rules | TestMatching | pass |
| internal/rules | TestOverlaps | pass |
| internal/rules | TestParseSize | pass |
| internal/rules | TestPathGlob | pass |
| internal/rules | TestReportsAllErrors | pass |
| internal/rules | TestRewriteGroupReferences | pass |
| internal/yamlite | TestLineNumbers | pass |
| internal/yamlite | TestParseErrors | pass |
| internal/yamlite | TestParseValid | pass |

## Latency

Over loopback, 20,000 sequential requests per row after a 2,000 request warm-up, then 16 clients for 2 seconds. The first three rows are patch with the legacy-customer example; the adapt rows are the customer-api example, where the list has three customers. The record row saves every exchange to a fixture file on disk.

| Path | p50 | p99 | Requests/s (16 clients) |
| --- | --- | --- | --- |
| direct to upstream | 45.1 µs | 337.2 µs | 23952 |
| proxy, no rule matches | 149.5 µs | 1155.1 µs | 5718 |
| proxy, both example rules applied | 219.0 µs | 2139.6 µs | 5159 |
| adapt: direct to v2, one customer | 52.0 µs | 379.0 µs | 20718 |
| adapt: v1 lookup rebuilt from v2 | 222.9 µs | 2359.2 µs | 4699 |
| adapt: direct to v2, list | 66.8 µs | 402.5 µs | 22907 |
| adapt: v1 list rebuilt from v2 | 297.9 µs | 2606.8 µs | 4228 |
| record: v2 lookup saved to a fixture | 249.4 µs | 1869.8 µs | 5239 |

Added by the proxy when no rule matches: 104.4 µs at p50, 817.9 µs at p99.
Added by the proxy with both example rules applied: 173.9 µs at p50, 1802.4 µs at p99.
Added by adapt rebuilding a v1 lookup from v2: 170.9 µs at p50, 1980.2 µs at p99.
Added by adapt rebuilding a v1 list from v2: 231.1 µs at p50, 2204.3 µs at p99.
Added by record saving a v2 lookup to a fixture file: 197.4 µs at p50, 1490.8 µs at p99.
