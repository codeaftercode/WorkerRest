#!/usr/bin/env python3
"""给 main 包文件里的下沉类型引用加 model. 前缀（跳过注释，含行内注释）。

用法：python scripts/prefix_model_types.py <文件列表>
"""
import re
import sys

TYPES = [
    "TakeoutRedPacket", "SupStageInfo", "SupLines", "Appearance",
    "SupGeom", "TakeoutInfo", "BreakInfo", "TimeSlot",
    # 第二轮：配置层 + 计划层类型
    "ReminderMessages", "ReminderMessageItem", "SupervisorConfig",
    "AppearanceConfig", "BreakConfig", "BreakEntry", "PlannedBreak",
    "PlanRef", "PlanSplit", "TimelineItem", "TakeoutPostponed",
    "SchedulerState", "AppPauseItem", "AppPauseConfig", "CustomBreak",
]


def prefix_line(line):
    if line.lstrip().startswith('//'):
        return line
    code = line
    comment = ''
    idx = line.find('//')
    if idx != -1 and (idx == 0 or line[idx-1] in ' \t'):
        code = line[:idx]
        comment = line[idx:]
    for t in TYPES:
        code = re.sub(r'(?<![\w.])' + t + r'\b', 'model.' + t, code)
    return code + comment


for p in sys.argv[1:]:
    with open(p, 'r', encoding='utf-8') as f:
        lines = f.read().split('\n')
    out = [prefix_line(l) for l in lines]
    with open(p, 'w', encoding='utf-8', newline='\n') as f:
        f.write('\n'.join(out))
    print('processed', p)
