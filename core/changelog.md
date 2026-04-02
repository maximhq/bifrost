- feat: add Fireworks AI as a first-class provider [@ivanetchart](https://github.com/ivanetchart)
[fix]: bedrock streaming - retry retryable AWS exceptions (503/429/500) by classifying them as IsBifrostError:false [@KTS-o7](https://github.com/KTS-o7)
- fix: bedrock streaming - retry stale/closed connections by classifying transport errors as IsBifrostError:false [@KTS-o7](https://github.com/KTS-o7)
- fix: case-insensitive `anthropic-beta` merge in `MergeBetaHeaders`