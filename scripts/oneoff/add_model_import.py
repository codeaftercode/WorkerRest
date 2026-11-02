#!/usr/bin/env python3
"""给 main 包文件插入 internal/model 的 import（只加 import，不做类型前缀替换）。

用法：python scripts/add_model_import.py <文件列表>
"""
import sys

IMPORT_PATH = 'workerrest/internal/model'


def add_import(text):
    if ('"%s"' % IMPORT_PATH) in text:
        return text
    lines = text.split('\n')
    out = []
    inserted = False
    for line in lines:
        st = line.strip()
        if st.startswith('import ('):
            out.append(line)
            out.append('\t"%s"' % IMPORT_PATH)
            inserted = True
        elif st.startswith('import "') or st.startswith('import _'):
            out.append(line)
            out.append('import "%s"' % IMPORT_PATH)
            inserted = True
        else:
            out.append(line)
    if not inserted:
        result = []
        for line in out:
            result.append(line)
            if line.startswith('package '):
                result.append('')
                result.append('import "%s"' % IMPORT_PATH)
        out = result
    return '\n'.join(out)


for p in sys.argv[1:]:
    with open(p, 'r', encoding='utf-8') as f:
        text = f.read()
    new = add_import(text)
    with open(p, 'w', encoding='utf-8', newline='\n') as f:
        f.write(new)
    print('processed', p)
