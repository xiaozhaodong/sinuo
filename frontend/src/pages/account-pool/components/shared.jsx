// Account Pool 共享表单组件

const FormField = ({ label, required = false, children }) => (
  <label className="flex items-start gap-3 px-3.5 py-2">
    <span className="w-[112px] shrink-0 pt-[3px] text-right text-[12.5px] text-fg-body">
      {label}{required && <span className="ml-1 text-danger">*</span>}
    </span>
    <span className="block min-w-0 flex-1">{children}</span>
  </label>
);

export { FormField };
