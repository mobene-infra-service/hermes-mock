// 回铃音占位：mock 坐席软电话不强依赖回铃音（验证链路为主）。
// 如需真实回铃，换成 wav 的 data URI 即可；为空时 SipCall 会跳过放音。
const ring = ''
export default ring
