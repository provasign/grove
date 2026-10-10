package invariance

// Group C: decoy invariant round 2, C family (fmt, nlohmann/json,
// AFNetworking). Each clean file is one the grammar could not parse; the
// decoy adds a comment line after every `{` and a block comment at the
// end, as research's decoy injector does. Error recovery, the line
// scanner's window and the namespace choice moved with those comments.

const cDecoyLine = "    // decoy: class Fake extends Base { void ghost() { helper(1); new Fake(); } } Widget::draw(x); don't 'quote\n"

const cDecoyTail = "/* decoy block: don't 'quote\nclass Fake2 { void ghost(); };\n*/\n"

func casesC() []twinCase {
	return []twinCase{
		{
			id: "C1-objc-availability-macros",
			clean: one("p.m", `#import <Foundation/Foundation.h>

typedef NS_ENUM(NSUInteger, AFSSLPinningMode) {
    AFSSLPinningModeNone,
    AFSSLPinningModeCertificate,
};

NS_ASSUME_NONNULL_BEGIN

@interface AFSecurityPolicy : NSObject
@property (nonatomic, strong) NSURLSessionTaskMetrics *metrics AF_API_AVAILABLE(ios(10), macosx(10.12));
- (instancetype)initWithMode:(AFSSLPinningMode)mode NS_DESIGNATED_INITIALIZER;
- (void)reset;
@end

@implementation AFSecurityPolicy
- (void)didFinish:(NSURLSessionTaskMetrics *)metrics AF_API_AVAILABLE(ios(10), macosx(10.12)) {
    [self reset];
}
- (void)reset {
}
@end

NS_ASSUME_NONNULL_END
`),
			decoy: one("p.m", `#import <Foundation/Foundation.h>

typedef NS_ENUM(NSUInteger, AFSSLPinningMode) {
`+cDecoyLine+`    AFSSLPinningModeNone,
    AFSSLPinningModeCertificate,
};

NS_ASSUME_NONNULL_BEGIN

@interface AFSecurityPolicy : NSObject
@property (nonatomic, strong) NSURLSessionTaskMetrics *metrics AF_API_AVAILABLE(ios(10), macosx(10.12));
- (instancetype)initWithMode:(AFSSLPinningMode)mode NS_DESIGNATED_INITIALIZER;
- (void)reset;
@end

@implementation AFSecurityPolicy
- (void)didFinish:(NSURLSessionTaskMetrics *)metrics AF_API_AVAILABLE(ios(10), macosx(10.12)) {
`+cDecoyLine+`    [self reset];
}
- (void)reset {
`+cDecoyLine+`}
@end

NS_ASSUME_NONNULL_END
`+cDecoyTail),
			want: []string{"S AFSecurityPolicy kind=class", "S AFSecurityPolicy.metrics kind=field",
				"S AFSecurityPolicy.initWithMode: kind=method", "E AFSecurityPolicy.didFinish: calls AFSecurityPolicy.reset"},
			wantNot: []string{"S if", "S AF_API_AVAILABLE", "S initWithMode:NS_DESIGNATED_INITIALIZER:"},
		},
		{
			id: "C2-cpp-export-macros",
			clean: one("gtest.h", `namespace testing {
namespace internal {

GMOCK_DEFINE_DEFAULT_ACTION_FOR_RETURN_TYPE_(unsigned char, '\0');
GMOCK_DEFINE_DEFAULT_ACTION_FOR_RETURN_TYPE_(float, 0);

class GTEST_API_ RE {
 public:
  static DOCTEST_CONSTEXPR size_type len = 24;
  RE(const char* regex) { Init(regex); }
 private:
  void Init(const char* regex);
};

bool IsAlpha(char ch) {
  return isalpha(ch) != 0;
}

}  // namespace internal
}  // namespace testing
`),
			decoy: one("gtest.h", `namespace testing {
`+cDecoyLine+`namespace internal {
`+cDecoyLine+`
GMOCK_DEFINE_DEFAULT_ACTION_FOR_RETURN_TYPE_(unsigned char, '\0');
GMOCK_DEFINE_DEFAULT_ACTION_FOR_RETURN_TYPE_(float, 0);

class GTEST_API_ RE {
`+cDecoyLine+` public:
  static DOCTEST_CONSTEXPR size_type len = 24;
  RE(const char* regex) { Init(regex); }
 private:
  void Init(const char* regex);
};

bool IsAlpha(char ch) {
`+cDecoyLine+`  return isalpha(ch) != 0;
}

}  // namespace internal
}  // namespace testing
`+cDecoyTail),
			want:    []string{"S testing::internal::RE kind=class", "S testing::internal::IsAlpha kind=function", "S testing::internal::RE::Init kind=method", "S testing::internal::RE::len kind=field"},
			wantNot: []string{"S GTEST_API_", "S GMOCK_DEFINE_DEFAULT_ACTION_FOR_RETURN_TYPE_"},
		},
	}
}
