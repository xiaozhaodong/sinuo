#!/bin/bash

# Sinuo 本地构建测试脚本
# 用于在推送标签前测试构建过程

set -e

# 颜色输出
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🚀 Sinuo 本地构建测试${NC}"
echo "======================================"

# 检查Go环境
if ! command -v go &> /dev/null; then
    echo -e "${RED}❌ Go未安装或未加入PATH${NC}"
    exit 1
fi

echo -e "${GREEN}✅ Go版本: $(go version)${NC}"

# 清理之前的构建
echo -e "${YELLOW}🧹 清理构建目录...${NC}"
rm -rf dist/

# 创建构建目录
mkdir -p dist/

# 获取版本信息
VERSION=${1:-"v0.0.0-test"}
BUILD_TIME=$(date -u '+%Y-%m-%d_%H:%M:%S_UTC')
COMMIT_HASH=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")

echo -e "${BLUE}📋 构建信息:${NC}"
echo "  版本: $VERSION"
echo "  时间: $BUILD_TIME"
echo "  提交: $COMMIT_HASH"
echo ""

# 构建目标平台
PLATFORMS=(
    "linux/amd64"
    "darwin/amd64"
    "darwin/arm64"
    "windows/amd64"
)

echo -e "${BLUE}🔨 开始构建...${NC}"

for PLATFORM in "${PLATFORMS[@]}"
do
    GOOS=${PLATFORM%/*}
    GOARCH=${PLATFORM#*/}
    
    # 设置二进制文件名
    BINARY_NAME="Sinuo"
    ARCHIVE_NAME="Sinuo-${GOOS}-${GOARCH}"
    
    if [ "$GOOS" = "windows" ]; then
        BINARY_NAME="Sinuo.exe"
    fi
    
    echo -e "${YELLOW}📦 构建 ${GOOS}/${GOARCH}...${NC}"
    
    # 设置CGO（简化版本，只对Linux启用）
    export CGO_ENABLED=0
    if [ "$GOOS" = "linux" ] && [ "$GOARCH" = "amd64" ]; then
        export CGO_ENABLED=1
    fi
    
    # 构建
    LDFLAGS="-s -w -X main.version=$VERSION -X main.date=$BUILD_TIME -X main.commit=$COMMIT_HASH"
    
    env GOOS=$GOOS GOARCH=$GOARCH go build \
        -ldflags "$LDFLAGS" \
        -o "dist/${ARCHIVE_NAME}/${BINARY_NAME}" \
        .
    
    if [ $? -ne 0 ]; then
        echo -e "${RED}❌ 构建 ${GOOS}/${GOARCH} 失败${NC}"
        exit 1
    fi
    
    # 复制资源文件
    cp README.md "dist/${ARCHIVE_NAME}/"
    cp CLAUDE.md "dist/${ARCHIVE_NAME}/"
    cp -r config "dist/${ARCHIVE_NAME}/"
    
    # 创建数据目录和说明
    mkdir -p "dist/${ARCHIVE_NAME}/data"
    cat > "dist/${ARCHIVE_NAME}/data/README.txt" << 'EOF'
数据存储目录
============

此目录用于存储应用数据：

📊 usage.db - SQLite数据库文件，存储请求使用记录和统计信息
     - 自动创建（首次启动时）
     - 包含请求日志、token使用量、成本统计
     - 可通过Web界面查看和导出

💡 重要提示：
   • 请确保此目录有读写权限
   • 建议定期备份数据库文件
   • 数据库路径可在 config/config.yaml 中配置
EOF
    
    # 创建启动脚本
    if [ "$GOOS" = "windows" ]; then
        cat > "dist/${ARCHIVE_NAME}/start.bat" << 'EOF'
@echo off
echo 🚀 Sinuo 启动检查...

if not exist "config\config.yaml" (
    echo ⚠️ 配置文件不存在，请先复制 config\example.yaml 到 config\config.yaml
    echo 💡 快速配置命令: copy config\example.yaml config\config.yaml
    pause
    exit /b 1
)

if not exist "data" (
    echo 📁 创建数据目录...
    mkdir data
)

echo ✅ 环境检查完成
echo 📊 数据库位置: data\usage.db
echo 🌐 Web界面: http://localhost:8010 (如已启用)
echo.

echo 🚀 启动 Sinuo...
Sinuo.exe -config config\config.yaml
pause
EOF
    else
        cat > "dist/${ARCHIVE_NAME}/start.sh" << 'EOF'
#!/bin/bash
echo "🚀 Sinuo 启动检查..."

if [ ! -f "config/config.yaml" ]; then
    echo "⚠️ 配置文件不存在，请先复制 config/example.yaml 到 config/config.yaml"
    echo "💡 快速配置命令: cp config/example.yaml config/config.yaml"
    exit 1
fi

if [ ! -d "data" ]; then
    echo "📁 创建数据目录..."
    mkdir -p data
fi

if [ ! -w "data" ]; then
    echo "⚠️ 数据目录没有写权限，尝试修复..."
    chmod 755 data
fi

echo "✅ 环境检查完成"
echo "📊 数据库位置: data/usage.db"
echo "🌐 Web界面: http://localhost:8010 (如已启用)"
echo ""

echo "🚀 启动 Sinuo..."
./Sinuo -config config/config.yaml
EOF
        chmod +x "dist/${ARCHIVE_NAME}/start.sh"
        chmod +x "dist/${ARCHIVE_NAME}/${BINARY_NAME}"
    fi
    
    # 打包
    cd dist/
    if [ "$GOOS" = "windows" ]; then
        zip -r "${ARCHIVE_NAME}.zip" "${ARCHIVE_NAME}/" > /dev/null
        echo -e "${GREEN}✅ 创建: ${ARCHIVE_NAME}.zip${NC}"
    else
        tar -czf "${ARCHIVE_NAME}.tar.gz" "${ARCHIVE_NAME}/" 
        echo -e "${GREEN}✅ 创建: ${ARCHIVE_NAME}.tar.gz${NC}"
    fi
    cd ..
done

# 生成校验和
echo -e "${YELLOW}🔐 生成校验和文件...${NC}"
cd dist/
sha256sum *.tar.gz *.zip > checksums.txt 2>/dev/null || shasum -a 256 *.tar.gz *.zip > checksums.txt
cd ..

echo ""
echo -e "${GREEN}🎉 构建完成！${NC}"
echo -e "${BLUE}📁 构建产物位于 dist/ 目录:${NC}"
ls -la dist/

echo ""
echo -e "${BLUE}🧪 测试版本信息:${NC}"
if [ -f "dist/Sinuo-$(uname -s | tr '[:upper:]' '[:lower:]')-amd64/Sinuo" ]; then
    dist/Sinuo-$(uname -s | tr '[:upper:]' '[:lower:]')-amd64/Sinuo -version
elif [ -f "dist/Sinuo-darwin-arm64/Sinuo" ] && [ "$(uname -m)" = "arm64" ]; then
    dist/Sinuo-darwin-arm64/Sinuo -version
else
    echo "请手动运行对应平台的二进制文件测试版本信息"
fi

echo ""
echo -e "${YELLOW}💡 提示:${NC}"
echo "  1. 测试各平台二进制文件的版本信息"
echo "  2. 确认配置文件和启动脚本正常"
echo "  3. 如果测试正常，可以推送标签触发自动发布："
echo "     git tag $VERSION"
echo "     git push origin $VERSION"
