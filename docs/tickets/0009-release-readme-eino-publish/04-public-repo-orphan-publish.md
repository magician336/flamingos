# 04:公开仓交付——orphan 提交、推送、spec 迁 issue、终检

**Spec:** [0009 发布就绪:Eino 示例、README 与公开仓交付](../../../specs/0009-release-readme-eino-publish.md)
**What to build:** 仓库从"私有开发仓"转为"可公开复用仓",计划 §9 发布交付清单全项兑现,目标 **github.com/magician336/flamingos 公开仓**。顺序六步:①发布卫生终检——全跟踪树按词表(教学、求职、面试、简历、课程等)grep 零命中,结果留痕;②orphan 无祖先初始提交——公开历史 = 当前 main 跟踪树(内容快照),树 = git ls-files 清单机械核对(零私有文件混入:MISSION/NOTES/lessons/reference/assets/learning-records/docs/plan/docs/research 等均不入树);③**将公开的完整文件清单交用户确认(硬性关口,确认前不推送)**;④经显式 URL 的临时 remote 推送公开仓(私有仓保持无 remote 本地状态、历史完整保留,git push 后临时 remote 移除);⑤仓库 description 与 topics 设置,八张 spec 迁为 GitHub issues(正文 = spec 全文,label 沿各 spec triage 同名词,frontmatter tracker-note 兑现;docs/specs 与 docs/tickets 文件保留在仓内不动);⑥公开仓复核——CI 工作流首跑绿(双 job:主模块测试门禁含 evalx 门禁 family、examples 构建校验;此为票 03 跨票交接验收的兑付点)、README 第一屏与链接在公开仓可达、issues 与 label 在位。降级顺序(spec 定稿):Eino 降 README 配置说明→CI 减步→推送单列;超时降级发生时 spec 修订表追记。
**Blocked by:** 01(Eino 示例)、02(README 全件 + LICENSE + Jaeger 截图 + 仓库卫生)、03(CI 工作流)——公开树快照前全部内容就位。
**Status:** ready-for-agent

- [ ] 卫生终检:全跟踪树词表 grep 零命中,结果留痕
- [ ] orphan 无祖先初始提交:树 = git ls-files 清单机械核对一致,零私有文件;私有仓历史与工作区不被推送动作污染
- [ ] 用户确认关口:将公开的完整文件清单经用户确认后才执行推送(票面留痕确认时点)
- [ ] 推送 github.com/magician336/flamingos 公开仓(临时 remote 用后即删),仓库 description 与 topics 就位
- [ ] 八张 spec 迁为 GitHub issues:正文为 spec 全文,label 沿 triage 同名词;仓内 docs/specs 与 docs/tickets 文件保留不动
- [ ] CI 首跑绿复核(票 03 交接验收兑付):双 job 全绿,evalx 门禁 family 在 CI 内执行
- [ ] 公开仓复核:README 第一屏、架构图渲染、截图与文档链接全部可达
- [ ] 时间盒降级是否发生的声明入 spec 0009 修订表(无论发生与否)
